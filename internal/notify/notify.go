// Package notify 把系统事件推送到用户配置的渠道。
//
// 设计取向：发送失败绝不影响主流程。通知是旁路，一个 Telegram token 过期
// 不该让实例关不了机。所有错误都记录在渠道行上，用户在通知页能看到。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"ocicore/internal/store"
)

// 事件类型。与设计规格里的"事件订阅矩阵"一一对应。
const (
	EventInstanceAnomaly = "instance.anomaly" // 实例状态异常变化
	EventAccountAuthFail = "account.auth"     // 账号认证失败
	EventQuotaNearLimit  = "quota.limit"      // 配额接近上限
	EventDiskNearFull    = "disk.full"        // 引导卷空间告警
	EventInstanceCreated = "instance.created" // 实例创建完成
	EventDangerOperation = "danger.operation" // 危险操作已执行
	EventHuntSucceeded   = "hunt.succeeded"   // 守候任务抢到了实例
	EventHuntStopped     = "hunt.stopped"     // 守候任务停止（失败/到期/配额满）
	EventCapacityChanged = "capacity.changed" // 容量监控项的状态发生变化
)

// EventDef 描述一个可订阅的事件。
type EventDef struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// EventDefs 返回全部可订阅事件，供前端渲染订阅矩阵。
func EventDefs() []EventDef {
	return []EventDef{
		{EventInstanceAnomaly, "实例状态异常变化", "实例被非预期地停止或进入故障状态"},
		{EventAccountAuthFail, "账号认证失败", "API 密钥失效或权限不足"},
		{EventQuotaNearLimit, "配额接近上限", "某项配额使用率超过 90%"},
		{EventDiskNearFull, "引导卷空间告警", "引导卷剩余空间不足"},
		{EventInstanceCreated, "实例创建完成", "新实例已就绪并分配到公网 IP"},
		{EventDangerOperation, "危险操作已执行", "终止实例、删除账号、更换 IP 等操作"},
		{EventHuntSucceeded, "守候任务抢到实例", "容量守候任务成功创建出实例"},
		{EventHuntStopped, "守候任务停止", "任务因配额已满、凭据失效或到期而停止"},
		{EventCapacityChanged, "容量状态变化", "监控的规格从没有容量变成有容量（或反之）"},
	}
}

// 支持的渠道类型。
const (
	KindTelegram = "telegram"
	KindWeCom    = "wecom"
	KindDingTalk = "dingtalk"
	KindEmail    = "email"
	KindWebhook  = "webhook"
)

// KindDef 描述一种渠道及其所需的配置字段。
type KindDef struct {
	Kind   string     `json:"kind"`
	Label  string     `json:"label"`
	Fields []FieldDef `json:"fields"`
}

// FieldDef 是渠道配置表单的一个字段。
type FieldDef struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
	Secret   bool   `json:"secret"`
	Hint     string `json:"hint,omitempty"`
}

// KindDefs 返回全部渠道类型定义，供前端动态渲染配置表单。
func KindDefs() []KindDef {
	return []KindDef{
		{Kind: KindTelegram, Label: "Telegram", Fields: []FieldDef{
			{Key: "token", Label: "Bot Token", Required: true, Secret: true,
				Hint: "向 @BotFather 申请"},
			{Key: "chatId", Label: "Chat ID", Required: true,
				Hint: "向 @userinfobot 发消息可获取"},
		}},
		{Kind: KindWeCom, Label: "企业微信", Fields: []FieldDef{
			{Key: "webhook", Label: "群机器人 Webhook", Required: true, Secret: true},
		}},
		{Kind: KindDingTalk, Label: "钉钉", Fields: []FieldDef{
			{Key: "webhook", Label: "群机器人 Webhook", Required: true, Secret: true},
			{Key: "secret", Label: "加签密钥", Secret: true, Hint: "启用加签时填写"},
		}},
		{Kind: KindEmail, Label: "邮件", Fields: []FieldDef{
			{Key: "host", Label: "SMTP 服务器", Required: true},
			{Key: "port", Label: "端口", Required: true, Hint: "通常是 587"},
			{Key: "username", Label: "用户名", Required: true},
			{Key: "password", Label: "密码", Required: true, Secret: true},
			{Key: "from", Label: "发件人", Required: true},
			{Key: "to", Label: "收件人", Required: true, Hint: "多个用逗号分隔"},
		}},
		{Kind: KindWebhook, Label: "自定义 Webhook", Fields: []FieldDef{
			{Key: "url", Label: "URL", Required: true},
			{Key: "method", Label: "方法", Hint: "留空为 POST"},
		}},
	}
}

// Message 是一条待发送的通知。
type Message struct {
	Event string
	Title string
	Body  string
	// Fields 是附加的键值信息，各渠道按自己的格式渲染。
	Fields map[string]string
}

// sendTimeout 是单次发送的超时。通知是旁路，不值得为它长时间挂着。
const sendTimeout = 15 * time.Second

// Dispatcher 把消息分发到所有订阅了该事件的渠道。
type Dispatcher struct {
	st     *store.Store
	client *http.Client
}

// NewDispatcher 创建分发器。
func NewDispatcher(st *store.Store) *Dispatcher {
	return &Dispatcher{
		st:     st,
		client: &http.Client{Timeout: sendTimeout},
	}
}

// Dispatch 把消息推送到所有启用且订阅了该事件的渠道。
//
// 不返回错误：调用方是业务主流程，通知失败不该影响它。
// 失败信息记录在渠道行上，用户在通知页能看到。
func (d *Dispatcher) Dispatch(ctx context.Context, msg Message) {
	channels, err := d.st.ListChannels(ctx)
	if err != nil {
		slog.Warn("读取通知渠道失败", "err", err)
		return
	}

	var wg sync.WaitGroup
	for i := range channels {
		ch := channels[i]
		if !ch.Enabled || !subscribes(ch.Events, msg.Event) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sendTimeout)
			defer cancel()

			errMsg := ""
			if err := d.Send(sendCtx, &ch, msg); err != nil {
				errMsg = err.Error()
				slog.Warn("发送通知失败", "channel", ch.Name, "kind", ch.Kind, "err", err)
			}
			if err := d.st.RecordChannelSend(sendCtx, ch.ID, errMsg); err != nil {
				slog.Debug("记录发送结果失败", "channel", ch.ID, "err", err)
			}
		}()
	}
	wg.Wait()
}

// Send 向单个渠道发送消息。测试按钮直接调用它。
func (d *Dispatcher) Send(ctx context.Context, ch *store.Channel, msg Message) error {
	switch ch.Kind {
	case KindTelegram:
		return d.sendTelegram(ctx, ch.Config, msg)
	case KindWeCom:
		return d.sendWeCom(ctx, ch.Config, msg)
	case KindDingTalk:
		return d.sendDingTalk(ctx, ch.Config, msg)
	case KindEmail:
		return d.sendEmail(ch.Config, msg)
	case KindWebhook:
		return d.sendWebhook(ctx, ch.Config, msg)
	default:
		return fmt.Errorf("不支持的渠道类型 %q", ch.Kind)
	}
}

func subscribes(events []string, event string) bool {
	for _, e := range events {
		if e == event {
			return true
		}
	}
	return false
}

// plainText 把消息渲染成纯文本，多数渠道都能用。
func plainText(msg Message) string {
	var b strings.Builder
	b.WriteString(msg.Title)
	if msg.Body != "" {
		b.WriteString("\n")
		b.WriteString(msg.Body)
	}
	for k, v := range msg.Fields {
		b.WriteString("\n")
		b.WriteString(k)
		b.WriteString("：")
		b.WriteString(v)
	}
	return b.String()
}

func (d *Dispatcher) sendTelegram(ctx context.Context, cfg map[string]string, msg Message) error {
	token, chatID := cfg["token"], cfg["chatId"]
	if token == "" || chatID == "" {
		return fmt.Errorf("Telegram 渠道缺少 token 或 chatId")
	}
	payload := map[string]any{
		"chat_id": chatID,
		"text":    plainText(msg),
	}
	return d.postJSON(ctx, "https://api.telegram.org/bot"+token+"/sendMessage", payload)
}

func (d *Dispatcher) sendWeCom(ctx context.Context, cfg map[string]string, msg Message) error {
	webhook := cfg["webhook"]
	if webhook == "" {
		return fmt.Errorf("企业微信渠道缺少 webhook")
	}
	payload := map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": plainText(msg)},
	}
	return d.postJSON(ctx, webhook, payload)
}

func (d *Dispatcher) sendDingTalk(ctx context.Context, cfg map[string]string, msg Message) error {
	webhook := cfg["webhook"]
	if webhook == "" {
		return fmt.Errorf("钉钉渠道缺少 webhook")
	}
	payload := map[string]any{
		"msgtype": "text",
		"text":    map[string]string{"content": plainText(msg)},
	}
	return d.postJSON(ctx, webhook, payload)
}

func (d *Dispatcher) sendWebhook(ctx context.Context, cfg map[string]string, msg Message) error {
	target := cfg["url"]
	if target == "" {
		return fmt.Errorf("Webhook 渠道缺少 url")
	}
	payload := map[string]any{
		"event":  msg.Event,
		"title":  msg.Title,
		"body":   msg.Body,
		"fields": msg.Fields,
		"source": "ocicore",
	}
	return d.postJSON(ctx, target, payload)
}

// postJSON 发送 JSON 请求并检查响应。
//
// 一并检查响应体里的业务错误码：企业微信和钉钉在参数错误时
// 依然返回 HTTP 200，只在 body 里写 errcode，只看状态码会漏掉。
func (d *Dispatcher) postJSON(ctx context.Context, url string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		OK      *bool  `json:"ok"`
		Desc    string `json:"description"`
	}
	if json.Unmarshal(body, &result) == nil {
		if result.ErrCode != 0 {
			return fmt.Errorf("渠道返回错误 %d: %s", result.ErrCode, result.ErrMsg)
		}
		if result.OK != nil && !*result.OK {
			return fmt.Errorf("渠道返回失败: %s", result.Desc)
		}
	}
	return nil
}

func (d *Dispatcher) sendEmail(cfg map[string]string, msg Message) error {
	host, port := cfg["host"], cfg["port"]
	username, password := cfg["username"], cfg["password"]
	from, to := cfg["from"], cfg["to"]

	if host == "" || port == "" || from == "" || to == "" {
		return fmt.Errorf("邮件渠道缺少必要配置")
	}

	recipients := make([]string, 0, 4)
	for _, addr := range strings.Split(to, ",") {
		if addr = strings.TrimSpace(addr); addr != "" {
			recipients = append(recipients, addr)
		}
	}
	if len(recipients) == 0 {
		return fmt.Errorf("邮件渠道没有有效的收件人")
	}

	var body bytes.Buffer
	fmt.Fprintf(&body, "From: %s\r\n", from)
	fmt.Fprintf(&body, "To: %s\r\n", strings.Join(recipients, ", "))
	fmt.Fprintf(&body, "Subject: [OCI Core] %s\r\n", msg.Title)
	body.WriteString("MIME-Version: 1.0\r\n")
	body.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	body.WriteString(plainText(msg))

	addr := host + ":" + port
	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}
	return smtp.SendMail(addr, auth, from, recipients, body.Bytes())
}
