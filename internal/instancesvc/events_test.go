package instancesvc

import (
	"testing"
	"time"
)

func TestBusDeliversToAllSubscribers(t *testing.T) {
	bus := NewBus()

	a, cancelA := bus.Subscribe()
	defer cancelA()
	b, cancelB := bus.Subscribe()
	defer cancelB()

	if got := bus.SubscriberCount(); got != 2 {
		t.Fatalf("订阅者数量 = %d，期望 2", got)
	}

	bus.Publish(Event{Type: EventInstanceUpdated, InstanceID: "i-1", State: "RUNNING"})

	for i, ch := range []<-chan Event{a, b} {
		select {
		case e := <-ch:
			if e.InstanceID != "i-1" || e.State != "RUNNING" {
				t.Errorf("订阅者 %d 收到的事件不正确: %+v", i, e)
			}
			if e.At.IsZero() {
				t.Errorf("订阅者 %d 收到的事件缺少时间戳", i)
			}
		case <-time.After(time.Second):
			t.Errorf("订阅者 %d 没有收到事件", i)
		}
	}
}

func TestBusUnsubscribeStopsDelivery(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe()

	cancel()
	if got := bus.SubscriberCount(); got != 0 {
		t.Fatalf("取消订阅后数量 = %d，期望 0", got)
	}

	// 取消后通道应当已关闭。
	select {
	case _, open := <-ch:
		if open {
			t.Error("取消订阅后通道仍在投递事件")
		}
	case <-time.After(time.Second):
		t.Error("取消订阅后通道未关闭")
	}

	// 对已取消的订阅重复取消不应 panic。
	cancel()

	// 没有订阅者时发布也不应阻塞或 panic。
	bus.Publish(Event{Type: EventSyncFinished})
}

// 一个卡住的浏览器标签页绝不能拖慢后台同步：
// 订阅者缓冲满了就丢事件，而不是阻塞 Publish。
func TestPublishNeverBlocksOnSlowSubscriber(t *testing.T) {
	bus := NewBus()
	_, cancel := bus.Subscribe()
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// 远超缓冲深度，若 Publish 会阻塞这里就永远发不完。
		for i := 0; i < subBuffer*4; i++ {
			bus.Publish(Event{Type: EventInstanceUpdated})
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Publish 在订阅者未消费时发生了阻塞")
	}
}
