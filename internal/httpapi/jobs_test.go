package httpapi

import (
	"net/http"
	"testing"
	"time"

	"ocicore/internal/store"
)

// 批量操作受理后返回任务；任务结束前不能删，结束后能在列表里看到并删除。
func TestJobLifecycleThroughAPI(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	resp := h.post("/api/instances/bulk", map[string]any{
		"instanceIds": []string{"nonexistent-instance"}, "action": "START",
	})
	expectStatus(t, resp, http.StatusAccepted)
	submitted := decode[struct {
		Job store.Job `json:"job"`
	}](t, resp)
	if submitted.Job.ID == "" || submitted.Job.Kind != jobBulkAction {
		t.Fatalf("受理响应里的任务不对：%+v", submitted.Job)
	}

	// 实例不存在，任务应当很快以失败结束，并带上逐台的原因。
	var job store.Job
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp = h.get("/api/jobs/" + submitted.Job.ID)
		expectStatus(t, resp, http.StatusOK)
		job = decode[store.Job](t, resp)
		if store.JobFinished(job.Status) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if job.Status != store.JobFailed || job.Error == "" || len(job.Steps) == 0 {
		t.Fatalf("任务应以失败结束并记录步骤：%+v", job)
	}

	resp = h.get("/api/jobs")
	expectStatus(t, resp, http.StatusOK)
	list := decode[struct {
		Jobs []store.Job `json:"jobs"`
	}](t, resp)
	if len(list.Jobs) != 1 || list.Jobs[0].ID != job.ID {
		t.Fatalf("列表应当只有这一条：%+v", list.Jobs)
	}

	resp = h.get("/api/jobs?status=active")
	expectStatus(t, resp, http.StatusOK)
	active := decode[struct {
		Jobs []store.Job `json:"jobs"`
	}](t, resp)
	if len(active.Jobs) != 0 {
		t.Fatalf("已结束的任务不应出现在进行中列表：%+v", active.Jobs)
	}

	resp = h.do("DELETE", "/api/jobs/"+job.ID, nil, true)
	expectStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	resp = h.get("/api/jobs/" + job.ID)
	expectErrorCode(t, resp, http.StatusNotFound, "not_found")
}

func TestDeleteRunningJobRejected(t *testing.T) {
	h := newHarness(t)
	h.setupAndLogin()

	now := time.Now()
	running := &store.Job{Kind: "test", Title: "进行中", Status: store.JobRunning,
		Steps: []store.JobStep{}, CreatedAt: now, StartedAt: &now}
	if err := h.store.SaveJob(t.Context(), running); err != nil {
		t.Fatal(err)
	}

	resp := h.do("DELETE", "/api/jobs/"+running.ID, nil, true)
	expectErrorCode(t, resp, http.StatusConflict, "job_running")
}
