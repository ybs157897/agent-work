package runnergateway

import "log"

// OnlineAdapterDesc 当前在线 runner 的 adapter manifest 摘要（hello 协商结果）。
type OnlineAdapterDesc struct {
	RunnerID       string
	AdapterVersion string
	SchemaDigest   string
}

// OnlineAdapters 列出当前在线、声明承接该 adapter 的 runner 摘要。
func (g *Gateway) OnlineAdapters(adapterID string) []OnlineAdapterDesc {
	g.mu.Lock()
	defer g.mu.Unlock()
	desc := make([]OnlineAdapterDesc, 0, len(g.conns))
	for _, rc := range g.conns {
		for _, a := range rc.adapters {
			if a.ID == adapterID {
				desc = append(desc, OnlineAdapterDesc{
					RunnerID: rc.runnerID, AdapterVersion: a.Version, SchemaDigest: a.SchemaDigest,
				})
			}
		}
	}
	return desc
}

// VerifyAdapterDigest 分派前能力校验：Run 的能力快照来自控制面本地 manifest，
// 远程 runner 实际执行的可能是另一版本——digest 不一致的在线 runner 被记 Warn
// 并视为不可承接；存在 digest 一致的承接者才返回 true。调用方（分派器）应在
// 返回 false 时回落进程内模块（若可用），否则以 capability_missing 拒绝分派。
// wantDigest 为空表示控制面没有该 adapter 的本地 manifest 真相，退化为仅在线性判断。
func (g *Gateway) VerifyAdapterDigest(adapterID, wantDigest string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	available := false
	for _, rc := range g.conns {
		for _, a := range rc.adapters {
			if a.ID != adapterID {
				continue
			}
			if wantDigest != "" && a.SchemaDigest != wantDigest {
				log.Printf("runnergateway: WARN runner %s 的 %s manifest digest 分叉（控制面 %s，runner 上报 %s），分派跳过该 runner",
					rc.runnerID, adapterID, wantDigest, a.SchemaDigest)
				continue
			}
			available = true
		}
	}
	return available
}
