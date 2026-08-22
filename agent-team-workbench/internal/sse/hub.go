// Package sse 提供 Workspace 事件流订阅（协议文档 §6）。
// Hub 只负责唤醒订阅者；实际补发从 EventRepo 按 cursor replay，
// 保证断线不丢事件、重复事件由 event_id 去重。
package sse

import "sync"

type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[chan struct{}]struct{})}
}

// Notify 唤醒 workspace 的所有订阅者（非阻塞）。
func (h *Hub) Notify(workspaceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[workspaceID] {
		select {
		case ch <- struct{}{}:
		default: // 慢客户端：丢弃唤醒，下一次心跳仍会 replay
		}
	}
}

// Subscribe 返回唤醒 channel 与取消函数。
func (h *Hub) Subscribe(workspaceID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs[workspaceID] == nil {
		h.subs[workspaceID] = make(map[chan struct{}]struct{})
	}
	h.subs[workspaceID][ch] = struct{}{}
	h.mu.Unlock()
	unsub := func() {
		h.mu.Lock()
		delete(h.subs[workspaceID], ch)
		h.mu.Unlock()
	}
	return ch, unsub
}
