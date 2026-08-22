package runtime

import "sync"

// Registry 是本进程 Adapter 注册表（M1 为控制平面内置 Mock；
// M2 起真实 Adapter 运行在 Runner 侧，注册表迁移为 manifest 协商）。
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]RuntimeAdapter
}

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]RuntimeAdapter)}
}

func (r *Registry) Register(adapterID string, adapter RuntimeAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[adapterID] = adapter
}

func (r *Registry) Get(adapterID string) (RuntimeAdapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[adapterID]
	return a, ok
}
