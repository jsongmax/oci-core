package ociclient

import (
	"encoding/json"
	"testing"
)

// TestPickPrimarySubscription 锁住多条订阅时的选择顺序。
func TestPickPrimarySubscription(t *testing.T) {
	cases := []struct {
		name        string
		subs        []Subscription
		wantService string
		wantPayment string
	}{
		{
			// issue #11：IDCS 排在前面且没有付费模式，必须跳过它选 CLOUDCM。
			name: "IDCS 在前时选 CLOUDCM",
			subs: []Subscription{
				{ServiceName: "IDCS", LifecycleState: "ACTIVE"},
				{ServiceName: "CLOUDCM", LifecycleState: "ACTIVE", PaymentModel: "PAYG"},
			},
			wantService: "CLOUDCM", wantPayment: "PAYG",
		},
		{
			name: "带付费模式的 CLOUDCM 优先于其它带付费模式的订阅",
			subs: []Subscription{
				{ServiceName: "OTHER", LifecycleState: "ACTIVE", PaymentModel: "PAYG"},
				{ServiceName: "CLOUDCM", LifecycleState: "ACTIVE", PaymentModel: "FREE_TRIAL"},
			},
			wantService: "CLOUDCM", wantPayment: "FREE_TRIAL",
		},
		{
			name: "没有 CLOUDCM 时选带付费模式的那条",
			subs: []Subscription{
				{ServiceName: "IDCS", LifecycleState: "ACTIVE"},
				{ServiceName: "OTHER", LifecycleState: "ACTIVE", PaymentModel: "PAY_AS_YOU_GO"},
			},
			wantService: "OTHER", wantPayment: "PAY_AS_YOU_GO",
		},
		{
			name: "已取消的订阅即便带付费模式也不优先",
			subs: []Subscription{
				{ServiceName: "CLOUDCM", LifecycleState: "CANCELED", PaymentModel: "FREE_TRIAL"},
				{ServiceName: "CLOUDCM", LifecycleState: "ACTIVE", PaymentModel: "PAYG"},
			},
			wantService: "CLOUDCM", wantPayment: "PAYG",
		},
		{
			name: "都没有付费模式时仍取第一条 ACTIVE",
			subs: []Subscription{
				{ServiceName: "A", LifecycleState: "CANCELED"},
				{ServiceName: "IDCS", LifecycleState: "ACTIVE"},
			},
			wantService: "IDCS",
		},
		{
			name: "全都不是 ACTIVE 时取第一条",
			subs: []Subscription{
				{ServiceName: "A", LifecycleState: "CANCELED", PaymentModel: "FREE_TRIAL"},
				{ServiceName: "B", LifecycleState: "CANCELED"},
			},
			wantService: "A", wantPayment: "FREE_TRIAL",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pickPrimarySubscription(tc.subs)
			if got == nil {
				t.Fatal("got nil")
			}
			if got.ServiceName != tc.wantService || got.PaymentModel != tc.wantPayment {
				t.Fatalf("got %s/%q, want %s/%q", got.ServiceName, got.PaymentModel, tc.wantService, tc.wantPayment)
			}
		})
	}

	if pickPrimarySubscription(nil) != nil {
		t.Fatal("空列表应返回 nil")
	}
}

// TestPickPrimarySubscriptionFromIssue11 用 issue #11 里接口的实际返回（脱敏）走一遍解码和选择。
// paymentModel 为 JSON null，过期的 endDate 不能让 CLOUDCM 落选。
func TestPickPrimarySubscriptionFromIssue11(t *testing.T) {
	body := `{"items":[
		{"serviceName":"IDCS","paymentModel":null,"lifecycleState":"ACTIVE","endDate":null},
		{"serviceName":"CLOUDCM","paymentModel":"PAYG","lifecycleState":"ACTIVE","endDate":"2021-12-14T15:17:06.000Z"}
	]}`
	var out struct {
		Items []Subscription `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	got := pickPrimarySubscription(out.Items)
	if got == nil || got.ServiceName != "CLOUDCM" || got.PaymentModel != "PAYG" {
		t.Fatalf("got %+v", got)
	}
	if got.IsFreeTrial() {
		t.Fatal("PAYG 不应被当成试用号")
	}
}
