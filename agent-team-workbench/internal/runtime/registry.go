package runtime

import "sync"

// Registry 是本进程 Adapter 注册表：条目只做能力协商（Manifest/Probe），
// 执行面统一在 ModuleRunner；真实 Adapter 也可运行在 Runner 侧（manifest 协商）。
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]ManifestProber
}

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]ManifestProber)}
}

func (r *Registry) Register(adapterID string, adapter ManifestProber) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[adapterID] = adapter
}

func (r *Registry) Get(adapterID string) (ManifestProber, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[adapterID]
	return a, ok
}
