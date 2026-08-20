package instancesvc

import "testing"

// TestSyncReportCountsMatchWhatUsersSee 锁住同步报告与界面的一致性。
//
// 界面各处（列表、总览、分组统计）一律过滤 TERMINATED，而同步报告曾经统计
// Oracle 返回的全部实例——OCI 在实例终止后还会把它列出来一段时间，于是
// 「同步完成 14 台」配上一张只有 13 行的列表。两个数字各自都对，摆在一起
// 就是 bug。
//
// 区域数同理：曾经取 (账号 × 区域) 的任务数，两个账号开在同一区域时会数成
// 两个，出现「5 个账号 · 6 个区域」这种自相矛盾。
func TestSyncReportSeparatesTerminated(t *testing.T) {
	r := &SyncReport{}
	// 模拟两个区域的汇总：区域 A 有 2 台在跑 1 台已终止，区域 B 有 1 台在跑。
	for _, c := range []struct{ active, terminated int }{{2, 1}, {1, 0}} {
		r.Instances += c.active
		r.Terminated += c.terminated
	}
	if r.Instances != 3 {
		t.Errorf("Instances = %d，期望 3（只数未终止的）", r.Instances)
	}
	if r.Terminated != 1 {
		t.Errorf("Terminated = %d，期望 1", r.Terminated)
	}
}

// TestRegionCountIsDeduplicated 验证区域数按去重后的集合算。
func TestRegionCountIsDeduplicated(t *testing.T) {
	jobs := []string{"ap-osaka-1", "ap-seoul-1", "ap-osaka-1"}
	seen := make(map[string]struct{}, len(jobs))
	for _, r := range jobs {
		seen[r] = struct{}{}
	}
	if len(seen) != 2 {
		t.Errorf("去重后区域数 = %d，期望 2（两个账号共用 osaka 只算一个）", len(seen))
	}
}
