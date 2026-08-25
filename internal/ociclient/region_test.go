package ociclient

import (
	"strings"
	"testing"
)

// TestEndpointHostNeverEscapesOracle 锁住「主机名不受输入控制」这个性质。
//
// TestNormalizeRegionRejectsHostInjection 验的是消毒函数本身；这一条验的是
// **拼接结果**——也就是 CodeQL 报 go/request-forgery 时指向的那个 sink。
// 消毒函数再对，只要有一条路径绕过 Endpoint，性质就没了。
func TestEndpointHostNeverEscapesOracle(t *testing.T) {
	hostile := []string{
		"evil.com/",
		"evil.com/x",
		"a@evil.com",
		"ap-osaka-1.evil.com",
		"host:8080",
		"a/../b",
		"a?x=1",
		"a#frag",
		"../../evil.com",
		"ap-osaka-1/../../evil.com",
	}

	for _, region := range hostile {
		got := Endpoint(ServiceCore, region)
		// 不管输入多恶意，主机名部分必须仍落在 oraclecloud 域内，
		// 且不能出现能改变 authority 的字符。
		if !strings.HasPrefix(got, "https://iaas.") {
			t.Errorf("region=%q 产生了非预期的前缀: %s", region, got)
		}
		if !strings.HasSuffix(got, ".oraclecloud.com") {
			t.Errorf("region=%q 让主机名逃出了 oraclecloud: %s", region, got)
		}
		if strings.Contains(got, "evil.com") {
			t.Errorf("region=%q 把攻击者域名带进了 URL: %s", region, got)
		}
		// 去掉协议头之后不该还有 / @ ? # —— 那几个都能改写 authority。
		rest := strings.TrimPrefix(got, "https://")
		if strings.ContainsAny(rest, "/@?#") {
			t.Errorf("region=%q 的结果里含改写 authority 的字符: %s", region, got)
		}
	}
}

// TestDoRejectsBadRegionBeforeRequest 非法区域要在发请求之前就被拒。
//
// 光靠 NormalizeRegion 返回空串，拼出来的是 https://iaas..oraclecloud.com，
// 请求照发、失败在 DNS 上，错误信息跟"区域填错了"毫无关系。
func TestDoRejectsBadRegionBeforeRequest(t *testing.T) {
	c, err := New(testCreds(t, testKey(t)))
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.Do(t.Context(), Request{
		Method: "GET", Service: ServiceCore, Path: "/instances", Region: "evil.com/",
	}, nil)
	if err == nil {
		t.Fatal("非法区域应当被拒，实际请求发出去了")
	}
	if !strings.Contains(err.Error(), "区域名不合法") {
		t.Errorf("错误信息应点明是区域问题，实际: %v", err)
	}
}
