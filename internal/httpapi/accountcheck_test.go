package httpapi

import (
	"testing"
	"time"

	"ocicore/internal/store"
)

func TestCheckDue(t *testing.T) {
	interval := 6 * time.Hour
	ago := func(d time.Duration) *time.Time {
		t := time.Now().Add(-d)
		return &t
	}

	cases := []struct {
		name string
		last *time.Time
		want bool
	}{
		// 从未校验过多半是导入后校验失败留下的，拖着不查只会让卡片
		// 一直停在"尚未校验"。
		{"从未校验", nil, true},
		{"刚查过", ago(time.Minute), false},
		{"差一点到期", ago(5*time.Hour + 59*time.Minute), false},
		{"刚好到期", ago(6 * time.Hour), true},
		{"早就该查了", ago(72 * time.Hour), true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			acc := &store.Account{LastCheckedAt: c.last}
			if got := checkDue(acc, interval); got != c.want {
				t.Errorf("checkDue = %v，期望 %v", got, c.want)
			}
		})
	}
}
