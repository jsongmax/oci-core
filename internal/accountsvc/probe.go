// Package accountsvc 提供账号层面的业务操作：连通性校验、凭据探测。
package accountsvc

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ocicore/internal/ociclient"
)

// Step 是连通性校验中的一个检查项。
//
// 对应设计稿里添加账号抽屉底部那串逐项亮起的勾——把"能不能用"拆成
// 用户看得懂的几步，比丢一个 401 出来有用得多。
type Step struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Result 是一次连通性校验的完整结果。
type Result struct {
	OK    bool   `json:"ok"`
	Steps []Step `json:"steps"`

	// 校验通过时填充，供前端预填表单与展示。
	UserName    string   `json:"userName,omitempty"`
	UserEmail   string   `json:"userEmail,omitempty"`
	TenancyName string   `json:"tenancyName,omitempty"`
	Regions     []string `json:"regions,omitempty"`
	HomeRegion  string   `json:"homeRegion,omitempty"`

	// 订阅信息。取不到就留空——精简权限的 IAM 用户读不了 organizations 服务,
	// 那不代表账号有问题。
	PaymentModel         string     `json:"paymentModel,omitempty"`
	SubscriptionState    string     `json:"subscriptionState,omitempty"`
	SubscriptionStartsAt *time.Time `json:"subscriptionStartsAt,omitempty"`
	SubscriptionEndsAt   *time.Time `json:"subscriptionEndsAt,omitempty"`

	// 校验失败时填充。ErrorCode 原样保留 Oracle 的错误码——用户要拿去搜。
	ErrorCode string `json:"errorCode,omitempty"`
	ErrorText string `json:"errorText,omitempty"`
	Advice    string `json:"advice,omitempty"`
	// AccountFatal 表示这是凭据本身的问题，账号应被标记为异常。
	AccountFatal bool `json:"accountFatal"`
}

// Draft 是一份尚未保存的凭据，用于"先测试再保存"的流程。
type Draft struct {
	TenancyOCID   string
	UserOCID      string
	Fingerprint   string
	PrivateKeyPEM string
	Region        string
	ProxyURL      string
}

// probeTimeout 是整个校验流程的上限。用户在等这个结果，不能让它挂太久。
const probeTimeout = 20 * time.Second

// CheckDraft 校验一份尚未保存的凭据。
func CheckDraft(ctx context.Context, d Draft) Result {
	var res Result

	key, err := ociclient.ParsePrivateKey([]byte(d.PrivateKeyPEM))
	if err != nil {
		res.Steps = append(res.Steps, Step{
			Key: "pem", Label: "私钥格式", OK: false, Detail: err.Error(),
		})
		res.ErrorText = err.Error()
		res.Advice = "请粘贴完整的 PEM 私钥，包含 BEGIN 与 END 两行。"
		return res
	}
	res.Steps = append(res.Steps, Step{Key: "pem", Label: "私钥格式有效", OK: true})

	creds := &ociclient.Credentials{
		TenancyOCID: strings.TrimSpace(d.TenancyOCID),
		UserOCID:    strings.TrimSpace(d.UserOCID),
		Fingerprint: strings.ToLower(strings.TrimSpace(d.Fingerprint)),
		Region:      ociclient.NormalizeRegion(d.Region),
		PrivateKey:  key,
	}

	if err := creds.Validate(); err != nil {
		res.Steps = append(res.Steps, Step{
			Key: "fingerprint", Label: "指纹与私钥匹配", OK: false, Detail: err.Error(),
		})
		res.ErrorText = err.Error()
		res.Advice = "指纹可以在 Oracle 控制台的「用户设置 → API 密钥」里核对。"
		return res
	}
	res.Steps = append(res.Steps, Step{
		Key: "fingerprint", Label: "指纹与私钥匹配", OK: true, Detail: creds.Fingerprint,
	})

	opts, err := clientOptions(d.ProxyURL)
	if err != nil {
		res.Steps = append(res.Steps, Step{Key: "proxy", Label: "代理配置", OK: false, Detail: err.Error()})
		res.ErrorText = err.Error()
		return res
	}

	client, err := ociclient.New(creds, opts...)
	if err != nil {
		res.ErrorText = err.Error()
		return res
	}
	return checkWithClient(ctx, client, res)
}

// CheckClient 用一个已建好的客户端执行在线校验，供已保存账号的复检使用。
func CheckClient(ctx context.Context, client *ociclient.Client) Result {
	res := Result{
		Steps: []Step{
			{Key: "pem", Label: "私钥格式有效", OK: true},
			{Key: "fingerprint", Label: "指纹与私钥匹配", OK: true},
		},
	}
	return checkWithClient(ctx, client, res)
}

// checkWithClient 执行需要联网的两步：取回用户、列出区域订阅。
func checkWithClient(ctx context.Context, client *ociclient.Client, res Result) Result {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	// GetUser 是最有价值的一次调用：它同时验证了签名算法、密钥有效性、
	// 系统时钟同步和网络可达性。这一步过了，凭据链路就是通的。
	user, err := client.GetCurrentUser(ctx)
	if err != nil {
		res.Steps = append(res.Steps, Step{
			Key: "identity", Label: "调用 GetUser", OK: false, Detail: shortError(err),
		})
		fillError(&res, err)
		return res
	}
	res.UserName = user.Name
	res.UserEmail = user.Email
	res.Steps = append(res.Steps, Step{
		Key: "identity", Label: "GetUser 调用成功", OK: true, Detail: userLabel(user),
	})

	// 订阅列表与租户名互不依赖，并发发出去。
	//
	// 不把 GetUser 也并进来是刻意的：凭据坏掉时只发一个请求就收手，
	// 而不是拿同一份坏签名连打三次——校验按钮是用户能反复点的，
	// 失败路径上的请求量要尽可能小。
	type tenancyResult struct {
		name string
		err  error
	}
	tenancyCh := make(chan tenancyResult, 1)
	go func() {
		tenancy, err := client.GetTenancy(ctx)
		if err != nil {
			tenancyCh <- tenancyResult{err: err}
			return
		}
		tenancyCh <- tenancyResult{name: tenancy.Name}
	}()

	subCh := make(chan *ociclient.Subscription, 1)
	go func() {
		sub, err := client.PrimarySubscription(ctx, "")
		if err != nil {
			// 读不到订阅是常态而非异常：organizations 服务需要单独的权限。
			slog.Debug("读取订阅信息失败", "err", err)
			subCh <- nil
			return
		}
		subCh <- sub
	}()

	subs, err := client.ListRegionSubscriptions(ctx)

	// 租户名让账号卡片能显示"这到底是哪个甲骨文账号"，而不是只有一串 OCID。
	// 取不到不影响结论——部分精简权限的 IAM 用户读不了租户对象。
	if t := <-tenancyCh; t.err == nil {
		res.TenancyName = t.name
	}
	// 订阅信息区分试用号与升级号。同样是"有则更好"，取不到不影响结论。
	if sub := <-subCh; sub != nil {
		res.PaymentModel = sub.PaymentModel
		res.SubscriptionState = sub.LifecycleState
		res.SubscriptionEndsAt = sub.EndDate
		// 优先用 TimeCreated：StartDate 是计费起始日，被抹到当天零点，
		// 算天数时会比实际多出小半天。
		res.SubscriptionStartsAt = sub.TimeCreated
		if res.SubscriptionStartsAt == nil {
			res.SubscriptionStartsAt = sub.StartDate
		}
	}

	if err != nil {
		// 订阅列表失败不影响"凭据可用"这个结论——多半是缺少读取租户的权限。
		res.Steps = append(res.Steps, Step{
			Key: "regions", Label: "读取区域订阅", OK: false, Detail: shortError(err),
		})
		res.OK = true
		res.Advice = "凭据可用，但无法读取区域订阅，可能缺少读取租户的权限。"
		return res
	}
	for _, sub := range subs {
		res.Regions = append(res.Regions, sub.RegionName)
		if sub.IsHomeRegion {
			res.HomeRegion = sub.RegionName
		}
	}
	res.Steps = append(res.Steps, Step{
		Key: "regions", Label: "已订阅 " + itoa(len(subs)) + " 个区域", OK: true,
		Detail: strings.Join(res.Regions, ", "),
	})

	res.OK = true
	return res
}

// clientOptions 根据代理配置构造 http.Client。
func clientOptions(proxyURL string) ([]ociclient.Option, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return nil, nil
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(u)
	return []ociclient.Option{
		ociclient.WithHTTPClient(&http.Client{Transport: transport, Timeout: 30 * time.Second}),
	}, nil
}

// fillError 把 API 错误拆成"原始错误码 + 人话建议"两部分，两者都要给用户。
func fillError(res *Result, err error) {
	res.OK = false
	if apiErr, ok := ociclient.AsAPIError(err); ok {
		res.ErrorCode = apiErr.Code
		if res.ErrorCode == "" {
			res.ErrorCode = "HTTP " + itoa(apiErr.StatusCode)
		}
		res.ErrorText = apiErr.Message
		res.Advice = apiErr.Advice()
		res.AccountFatal = apiErr.Class.AccountFatal()
		return
	}
	res.ErrorText = err.Error()
}

func shortError(err error) string {
	if apiErr, ok := ociclient.AsAPIError(err); ok {
		code := apiErr.Code
		if code == "" {
			code = "HTTP " + itoa(apiErr.StatusCode)
		}
		return code + " · " + apiErr.Message
	}
	return err.Error()
}

func userLabel(u *ociclient.User) string {
	if u.Email != "" {
		return u.Email
	}
	return u.Name
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
