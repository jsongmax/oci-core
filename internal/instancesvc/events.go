package instancesvc

import (
	"sync"
	"time"
)

// 事件类型。前端通过 SSE 订阅它们来实时更新界面，
// 而不是靠轮询——生命周期转换要 30–90 秒，轮询既慢又浪费。
const (
	EventInstanceUpdated = "instance.updated"
	EventInstanceRemoved = "instance.removed"
	EventInstanceError   = "instance.error"
	EventAccountStatus   = "account.status"
	EventSyncStarted     = "sync.started"
	EventSyncFinished    = "sync.finished"
)

// Event 是一条推给前端的实时事件。
type Event struct {
	Type       string    `json:"type"`
	At         time.Time `json:"at"`
	InstanceID string    `json:"instanceId,omitempty"`
	AccountID  string    `json:"accountId,omitempty"`
	State      string    `json:"state,omitempty"`
	Message    string    `json:"message,omitempty"`
	Data       any       `json:"data,omitempty"`
}

// subBuffer 是每个订阅者的缓冲深度。
//
// 一次全量同步可能瞬间产生几十条事件，缓冲太浅会让正常的浏览器订阅
// 也开始丢事件；太深则会在客户端卡死时占住内存。
const subBuffer = 64

// Bus 是进程内的事件广播。
//
// 刻意不做持久化：SSE 断线重连后前端会重新拉一次全量列表，
// 补发历史事件既复杂又没有必要。
type Bus struct {
	mu   sync.Mutex
	subs map[int]chan Event
	next int
}

// NewBus 创建事件总线。
func NewBus() *Bus {
	return &Bus{subs: make(map[int]chan Event)}
}

// Subscribe 注册一个订阅者，返回事件通道与取消函数。
// 调用方必须在结束时调用取消函数，否则订阅会一直占着。
func (b *Bus) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.next
	b.next++
	ch := make(chan Event, subBuffer)
	b.subs[id] = ch

	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
}

// Publish 向所有订阅者广播。
//
// 对满了的订阅者直接丢弃事件而不是阻塞：一个卡住的浏览器标签页
// 绝不能拖慢后台的同步循环。丢了也不要紧——前端本来就会定期兜底刷新。
func (b *Bus) Publish(e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// SubscriberCount 返回当前订阅者数量，用于诊断。
func (b *Bus) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
