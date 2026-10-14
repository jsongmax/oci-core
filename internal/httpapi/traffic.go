package httpapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"ocicore/internal/ociclient"
)

// trafficDay 是实例本月流量按 UTC 日期拆开的一天。
type trafficDay struct {
	Date     string  `json:"date"`
	OutBytes float64 `json:"outBytes"`
	InBytes  float64 `json:"inBytes"`
}

// handleInstanceTraffic 返回实例本自然月（UTC）的已用流量。
//
// 数据取自 oci_vcn 命名空间的网卡指标，按小时求和后再折成天。
// 选它而不是 oci_computeagent 的 NetworksBytesOut：网卡指标由 VCN 在宿主侧统计，
// 不依赖实例里的 Oracle Cloud Agent，而且直接给出字节数，不用从速率反推。
//
// 它不是计费口径：VCN 内网和同区域 OCI 服务的流量也算在里面。
// 计费口径的出站流量在账单页（Usage API）。
func (s *Server) handleInstanceTraffic(w http.ResponseWriter, r *http.Request) {
	inst, err := s.st.GetInstance(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	client, acc, err := s.conns.ForID(r.Context(), inst.AccountID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	compartment := orText(inst.CompartmentID, orText(acc.CompartmentOCID, acc.TenancyOCID))
	atts, err := client.ListVnicAttachments(ctx, inst.Region, compartment, inst.ID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "oci", shortOCIError(err))
		return
	}
	vnics := make([]string, 0, len(atts))
	for _, att := range atts {
		// 已分离的网卡不再产生流量，但本月早些时候的流量仍算数；
		// 只排除还没挂上的，避免查一块从没用过的网卡。
		if att.VnicID != "" && att.LifecycleState != "ATTACHING" {
			vnics = append(vnics, att.VnicID)
		}
	}

	now := time.Now().UTC()
	start := ociclient.AlignMonth(now)
	end := now

	type job struct {
		metric string
		vnic   string
	}
	jobs := make([]job, 0, len(vnics)*2)
	for _, v := range vnics {
		jobs = append(jobs,
			job{ociclient.MetricVnicToNetworkBytes, v},
			job{ociclient.MetricVnicFromNetworkBytes, v})
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		points   = make(map[string][]ociclient.Datapoint) // metric → 全部网卡的点
		firstErr string
		failed   int
	)
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			series, err := client.SummarizeMetrics(ctx, ociclient.MetricQuery{
				CompartmentID: compartment,
				Region:        inst.Region,
				Namespace:     ociclient.NamespaceVCN,
				Query:         ociclient.VnicMetricQuery(j.metric, j.vnic, "1h"),
				StartTime:     start,
				EndTime:       end,
				Resolution:    "1h",
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				if firstErr == "" {
					firstErr = shortOCIError(err)
				}
				return
			}
			for _, sr := range series {
				points[j.metric] = append(points[j.metric], sr.AggregatedDatapoints...)
			}
		}(j)
	}
	wg.Wait()

	series, outBytes, inBytes, out24h, in24h := foldTraffic(
		points[ociclient.MetricVnicToNetworkBytes],
		points[ociclient.MetricVnicFromNetworkBytes],
		start, now)

	resp := map[string]any{
		"instanceId":      inst.ID,
		"month":           start.Format("2006-01"),
		"start":           start,
		"end":             end,
		"vnics":           len(vnics),
		"outBytes":        outBytes,
		"inBytes":         inBytes,
		"last24hOutBytes": out24h,
		"last24hInBytes":  in24h,
		"series":          series,
		"notice":          "取自 VCN 网卡指标，包含 VCN 内网与同区域 OCI 服务的流量，比账单里的计费出站流量偏大。计费口径以账单页为准。",
	}
	// 部分网卡查失败时照样返回已有的数，同时带上错误；全部失败才算整体失败。
	if failed > 0 {
		resp["error"] = firstErr
	}
	if len(jobs) > 0 && failed == len(jobs) {
		writeError(w, http.StatusBadGateway, "oci", firstErr)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// foldTraffic 把按小时的出入站数据点折成按 UTC 日期的序列，并求出本月与最近 24 小时的合计。
//
// 日序列补齐没有数据的日子：停机的那几天也该占一格，显示为零。
func foldTraffic(out, in []ociclient.Datapoint, start, now time.Time) (series []trafficDay, outSum, inSum, out24h, in24h float64) {
	byDay := make(map[string]*trafficDay)
	day := func(t time.Time) *trafficDay {
		key := t.UTC().Format("2006-01-02")
		d := byDay[key]
		if d == nil {
			d = &trafficDay{Date: key}
			byDay[key] = d
		}
		return d
	}
	since := now.Add(-24 * time.Hour)

	for _, p := range out {
		day(p.Timestamp).OutBytes += p.Value
		outSum += p.Value
		if p.Timestamp.After(since) {
			out24h += p.Value
		}
	}
	for _, p := range in {
		day(p.Timestamp).InBytes += p.Value
		inSum += p.Value
		if p.Timestamp.After(since) {
			in24h += p.Value
		}
	}

	series = make([]trafficDay, 0, 31)
	for d := ociclient.AlignDay(start); !d.After(now); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		if v, ok := byDay[key]; ok {
			series = append(series, *v)
		} else {
			series = append(series, trafficDay{Date: key})
		}
	}
	return series, outSum, inSum, out24h, in24h
}
