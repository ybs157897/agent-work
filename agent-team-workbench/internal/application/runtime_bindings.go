package application

import (
	"context"
	"fmt"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// ── RuntimeBinding：Runtime 连接与模型配置（设置页）──────────────────

type CreateBindingParams struct {
	RuntimeLabel  string
	AdapterID     string
	Provider      string
	Model         string
	CredentialRef string
}

func (s *Service) CreateRuntimeBinding(ctx context.Context, workspaceID string, p CreateBindingParams) (*domain.RuntimeBinding, error) {
	if p.RuntimeLabel == "" || p.AdapterID == "" {
		return nil, fmt.Errorf("%w: runtime_label and adapter_id required", domain.ErrValidation)
	}
	manifest, adapterVersion := s.lookupManifest(ctx, p.AdapterID)
	now := time.Now().UTC()
	b := &domain.RuntimeBinding{
		ID: domain.NewID(domain.PrefixBinding), WorkspaceID: workspaceID,
		RuntimeLabel: p.RuntimeLabel, AdapterID: p.AdapterID, AdapterVersion: adapterVersion,
		Provider: p.Provider, Model: p.Model, CredentialRef: p.CredentialRef,
		Capabilities: manifest, Status: domain.BindingUnavailable,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		if err := s.store.Bindings().Create(ctx, b); err != nil {
			return err
		}
		if err := s.emit(ctx, workspaceID, domain.EventRuntimeHealthChanged,
			domain.AggregateRuntimeBinding, b.ID, b.Version, nil,
			map[string]any{"runtime_label": b.RuntimeLabel, "status": string(b.Status)}); err != nil {
			return err
		}
		return s.activity(ctx, workspaceID, "runtime.created",
			fmt.Sprintf("添加 Runtime %s（模型 %s/%s）", b.RuntimeLabel, b.Provider, b.Model))
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(workspaceID)
	return b, nil
}

// UpdateRuntimeBinding 修改模型配置（provider / model / credential_ref），乐观锁保护。
func (s *Service) UpdateRuntimeBinding(ctx context.Context, bindingID string, patch BindingPatch) (*domain.RuntimeBinding, error) {
	var updated *domain.RuntimeBinding
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		b, err := s.store.Bindings().Get(ctx, bindingID)
		if err != nil {
			return err
		}
		if err := b.CheckVersion(patch.ExpectedVersion); err != nil {
			return err
		}
		expected := b.Version
		if patch.Provider != nil {
			b.Provider = *patch.Provider
		}
		if patch.Model != nil {
			b.Model = *patch.Model
		}
		if patch.CredentialRef != nil {
			b.CredentialRef = *patch.CredentialRef
		}
		b.UpdatedAt = time.Now().UTC()
		if err := s.store.Bindings().Update(ctx, b, expected); err != nil {
			return err
		}
		b.Version++
		if err := s.emit(ctx, b.WorkspaceID, domain.EventRuntimeHealthChanged,
			domain.AggregateRuntimeBinding, b.ID, b.Version, nil,
			map[string]any{"runtime_label": b.RuntimeLabel, "model": b.Model}); err != nil {
			return err
		}
		// 凭据/模型配置变更必须入审计；只记引用，绝不记明文。
		s.audit(ctx, b.WorkspaceID, "runtime_binding.updated", b.ID,
			map[string]any{"model": b.Model, "credential_ref": b.CredentialRef})
		updated = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(updated.WorkspaceID)
	return updated, nil
}

type BindingPatch struct {
	Provider        *string
	Model           *string
	CredentialRef   *string
	ExpectedVersion int
}

func (s *Service) RuntimeBindings(ctx context.Context, workspaceID string) ([]*domain.RuntimeBinding, error) {
	return s.store.Bindings().List(ctx, workspaceID)
}

// ProbeRuntimeBinding 探测版本、认证、协议与能力；不启动业务 Run（协议文档 §5.3）。
func (s *Service) ProbeRuntimeBinding(ctx context.Context, bindingID string) (*domain.RuntimeBinding, *runtime.ProbeResult, error) {
	b, err := s.store.Bindings().Get(ctx, bindingID)
	if err != nil {
		return nil, nil, err
	}
	adapter, ok := s.adapters.Get(b.AdapterID)
	result := &runtime.ProbeResult{}
	if !ok {
		result.OK = false
		result.Error = "adapter not registered: " + b.AdapterID
	} else {
		r, err := adapter.Probe(ctx, runtime.ProbeRequest{WorkspaceID: b.WorkspaceID})
		if err != nil {
			result.OK = false
			result.Error = err.Error()
		} else {
			result = &r
		}
	}

	status := domain.BindingUnavailable
	if result.OK {
		status = domain.BindingReady
	}
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		expected := b.Version
		b.Status = status
		if result.Manifest != nil {
			b.ProviderVersion = result.Manifest.ProviderVersion
			b.AdapterVersion = result.Manifest.AdapterVersion
			caps := make(map[string]string, len(result.Manifest.Capabilities))
			for k, v := range result.Manifest.Capabilities {
				caps[k] = string(v)
			}
			b.Capabilities = caps
		}
		b.UpdatedAt = time.Now().UTC()
		if err := s.store.Bindings().Update(ctx, b, expected); err != nil {
			return err
		}
		b.Version++
		return s.emit(ctx, b.WorkspaceID, domain.EventRuntimeHealthChanged,
			domain.AggregateRuntimeBinding, b.ID, b.Version, nil,
			map[string]any{"status": string(status), "ok": result.OK})
	})
	if err != nil {
		return nil, nil, err
	}
	s.notifier.Notify(b.WorkspaceID)
	return b, result, nil
}

// lookupManifest 返回已注册 Adapter 的能力快照（未注册返回空）。
func (s *Service) lookupManifest(ctx context.Context, adapterID string) (map[string]string, string) {
	if s.adapters == nil {
		return nil, ""
	}
	adapter, ok := s.adapters.Get(adapterID)
	if !ok {
		return nil, ""
	}
	m, err := adapter.Manifest(ctx)
	if err != nil {
		return nil, ""
	}
	caps := make(map[string]string, len(m.Capabilities))
	for k, v := range m.Capabilities {
		caps[k] = string(v)
	}
	return caps, m.AdapterVersion
}
