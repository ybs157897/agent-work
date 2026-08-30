package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ensureCoordinatorRuntimeReady lazily probes a configured system Runtime at
// the durable Task hand-off boundary. Built-in bindings are created as
// unavailable until probed; requiring a separate settings click would violate
// create-and-auto-start semantics, while dispatching an unprobed binding would
// bypass the binding health contract.
func (s *Service) ensureCoordinatorRuntimeReady(ctx context.Context, state *domain.TaskCoordinatorState) error {
	if state == nil {
		return fmt.Errorf("%w: coordinator state required", domain.ErrValidation)
	}
	config, err := s.store.TaskCoordinators().GetConfig(ctx, state.WorkspaceID)
	if err != nil {
		return err
	}
	label := config.RuntimeLabel
	if useFallback, _ := state.Data["use_fallback"].(bool); useFallback && config.FallbackRuntimeLabel != "" {
		label = config.FallbackRuntimeLabel
	}
	binding, err := s.store.Bindings().GetByLabel(ctx, state.WorkspaceID, label)
	if err != nil {
		return fmt.Errorf("%w: Coordinator Runtime %q 未配置", domain.ErrValidation, label)
	}
	if !domain.TaskCoordinatorRuntimeMatchesAdapter(label, binding.AdapterID) {
		return fmt.Errorf("%w: Coordinator Runtime %q 与 adapter %q 不匹配", domain.ErrValidation, label, binding.AdapterID)
	}
	if binding.Status == domain.BindingReady {
		return nil
	}
	if s.adapters == nil {
		return fmt.Errorf("%w: Coordinator Runtime %q 无可用探测器", domain.ErrValidation, label)
	}
	probed, result, err := s.ProbeRuntimeBinding(ctx, binding.ID)
	if err != nil {
		if errors.Is(err, domain.ErrVersionConflict) {
			current, currentErr := s.store.Bindings().GetByLabel(ctx, state.WorkspaceID, label)
			if currentErr == nil && current.Status == domain.BindingReady {
				return nil
			}
		}
		return err
	}
	if probed != nil && probed.Status == domain.BindingReady && result != nil && result.OK {
		return nil
	}
	detail := "探测失败"
	if result != nil && strings.TrimSpace(result.Error) != "" {
		detail = strings.TrimSpace(result.Error)
	}
	return fmt.Errorf("%w: Coordinator Runtime %q 尚未就绪：%s", domain.ErrValidation, label, detail)
}
