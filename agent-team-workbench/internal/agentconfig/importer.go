package agentconfig

import (
	"context"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// Importer 把 agents/ 目录导入为 DB 投影（启动与 reload 时调用）。
type Importer struct {
	dir   string
	store application.Store
}

func NewImporter(dir string, store application.Store) *Importer {
	return &Importer{dir: dir, store: store}
}

type ImportResult struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
}

// Import 按 slug 对齐：已存在同 slug → 覆盖配置字段；同名未关联 → 关联 slug 并覆盖；
// 否则新建。availability/presence 等运行态字段不动。
func (im *Importer) Import(ctx context.Context, workspaceID string) (ImportResult, error) {
	var res ImportResult
	configs, err := LoadDir(im.dir)
	if err != nil {
		return res, err
	}
	if len(configs) == 0 {
		return res, nil
	}
	agents, err := im.store.Agents().List(ctx, workspaceID)
	if err != nil {
		return res, err
	}
	bySlug := map[string]*domain.AgentProfile{}
	byName := map[string]*domain.AgentProfile{}
	for _, a := range agents {
		if a.Slug != "" {
			bySlug[a.Slug] = a
		}
		byName[strings.ToLower(a.Name)] = a
	}

	for _, cfg := range configs {
		existing := bySlug[cfg.Slug]
		if existing == nil {
			existing = byName[strings.ToLower(cfg.Name)]
		}
		if existing == nil {
			now := time.Now().UTC()
			a := &domain.AgentProfile{
				ID: domain.NewID(domain.PrefixAgent), WorkspaceID: workspaceID,
				Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
				Version: 1, CreatedAt: now, UpdatedAt: now,
			}
			cfg.ToProfile(a)
			if err := im.store.Agents().Create(ctx, a); err != nil {
				return res, err
			}
			res.Created++
			continue
		}
		cfg.ToProfile(existing)
		expected := existing.Version
		existing.UpdatedAt = time.Now().UTC()
		if err := im.store.Agents().Update(ctx, existing, expected); err != nil {
			if err == domain.ErrVersionConflict {
				res.Skipped++
				continue
			}
			return res, err
		}
		res.Updated++
	}
	return res, nil
}

// WriteBackOne 把单个 Agent 的当前配置回写文件；未关联 slug 时分配并持久化。
func (im *Importer) WriteBackOne(ctx context.Context, a *domain.AgentProfile) error {
	taken := map[string]bool{}
	if configs, err := LoadDir(im.dir); err == nil {
		for _, c := range configs {
			taken[c.Slug] = true
		}
	}
	if a.Slug != "" {
		taken[a.Slug] = false // 自己的目录可复用
	}
	slug, err := WriteBack(im.dir, a, taken)
	if err != nil {
		return err
	}
	if a.Slug == slug {
		return nil
	}
	// 持久化新分配的 slug（配置字段已在本次 PATCH 中写入）。
	a.Slug = slug
	expected := a.Version
	a.UpdatedAt = time.Now().UTC()
	return im.store.Agents().Update(ctx, a, expected)
}
