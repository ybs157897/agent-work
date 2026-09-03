package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
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
	if handoff, _, todo, targetAgentID, handoffErr := s.handoffForCoordinatorStart(ctx, state); handoffErr != nil {
		return handoffErr
	} else if handoff != nil {
		if todo == nil || targetAgentID == "" {
			return fmt.Errorf("%w: Handoff target continuation is incomplete", domain.ErrStateConflict)
		}
		label := ""
		pinned := false
		if handoff.Target.Kind == domain.GovernanceActorRuntime {
			label, pinned = handoff.Target.ID, true
		} else {
			targetAgent, agentErr := s.store.Agents().Get(ctx, targetAgentID)
			if agentErr != nil {
				return agentErr
			}
			for _, candidate := range orchestrator.ResolveRuntimeCandidates(nil, targetAgent) {
				if _, bindingErr := s.store.Bindings().GetByLabel(ctx, state.WorkspaceID, candidate); bindingErr == nil {
					label = candidate
					break
				}
			}
			if label == "" {
				label = orchestrator.DefaultRuntimeLabel
			}
		}
		return s.ensureCoordinatorTargetBindingReady(ctx, state.WorkspaceID, label, pinned)
	}
	config, err := s.store.TaskCoordinators().GetConfig(ctx, state.WorkspaceID)
	if err != nil {
		return err
	}
	label := config.RuntimeLabel
	if useFallback, _ := state.Data["use_fallback"].(bool); useFallback && config.FallbackRuntimeLabel != "" {
		label = config.FallbackRuntimeLabel
	}
	if state.RepairStatus == domain.CoordinatorRepairPending && coordinatorControlAction(state) == "repair_plan" {
		repairOfRunID, _ := state.Data["repair_of_run_id"].(string)
		if repairOfRunID == "" {
			repairOfRunID = state.RepairSourceRunID
		}
		source, err := s.store.Runs().Get(ctx, repairOfRunID)
		if err != nil {
			return err
		}
		if err := s.validateGovernedRepairSource(ctx, state, source); err != nil {
			return err
		}
		label = source.RuntimeLabel
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

func (s *Service) ensureCoordinatorTargetBindingReady(ctx context.Context, workspaceID, label string, pinned bool) error {
	binding, err := s.store.Bindings().GetByLabel(ctx, workspaceID, label)
	if err != nil {
		return fmt.Errorf("%w: Handoff target Runtime %q 未配置", domain.ErrValidation, label)
	}
	if binding.Status == domain.BindingReady {
		return nil
	}
	if s.adapters == nil {
		return fmt.Errorf("%w: Handoff target Runtime %q 无可用探测器", domain.ErrValidation, label)
	}
	probed, result, err := s.ProbeRuntimeBinding(ctx, binding.ID)
	if err != nil {
		if errors.Is(err, domain.ErrVersionConflict) {
			current, currentErr := s.store.Bindings().GetByLabel(ctx, workspaceID, label)
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
	if pinned {
		return fmt.Errorf("%w: Handoff target Runtime %q 尚未就绪：%s", domain.ErrValidation, label, detail)
	}
	return fmt.Errorf("%w: Handoff target Runtime candidate %q 尚未就绪：%s", domain.ErrValidation, label, detail)
}
