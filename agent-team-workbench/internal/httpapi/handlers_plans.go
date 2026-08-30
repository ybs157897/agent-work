// handlers_plans.go M1 编排端点：plan 提交（幂等 + 同步执行）、plan 读取、
// work item 树（设计 note §API 契约：校验失败一律 400，不沿用 422 缺省）。
package httpapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// planProblemBytes 把 plan 提交的领域校验错误映射为 400（契约钉死），
// 其余错误沿用通用 problemBytes 语义。
func planProblemBytes(err error) (int, []byte) {
	if errors.Is(err, domain.ErrValidation) {
		return renderProblem(http.StatusBadRequest, "validation_failed", "Validation failed", err.Error())
	}
	return problemBytes(err)
}

// handleCreatePlan 提交并同步执行一份 plan（dispatch/defer/finish）；
// Idempotency-Key + PermWorkItemWrite，201 返回含步骤执行结果的 PlanDTO。
func (s *Server) handleCreatePlan(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("workspace_id")
	s.idempotent(w, r, wsID, func() (int, []byte) {
		var req createPlanRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		wi, err := s.store.WorkItems().Get(r.Context(), req.WorkItemID)
		if err != nil {
			return problemBytes(err)
		}
		if err := requireTaskWorkItemHTTP(wi); err != nil {
			return planProblemBytes(err)
		}
		if _, err := s.store.TaskCoordinators().GetStateForWorkItem(r.Context(), wi.ID); err == nil {
			return planProblemBytes(fmt.Errorf("%w: coordinated Task 的 plan 只由系统 Coordinator 内部提交", domain.ErrValidation))
		} else if !errors.Is(err, domain.ErrNotFound) {
			return problemBytes(err)
		}
		p := application.SubmitPlanParams{
			WorkItemID:     req.WorkItemID,
			AgentProfileID: req.AgentProfileID,
			SourceRunID:    req.SourceRunID,
			Steps:          make([]application.PlanStepInput, 0, len(req.Steps)),
		}
		if req.Guardrails != nil {
			p.Guardrails = *req.Guardrails
		}
		for _, raw := range req.Steps {
			step := application.PlanStepInput{Payload: map[string]any{}}
			for k, v := range raw {
				if k == "verb" {
					step.Verb, _ = v.(string)
					continue
				}
				step.Payload[k] = v
			}
			p.Steps = append(p.Steps, step)
		}
		plan, err := s.svc.SubmitPlan(r.Context(), wsID, p)
		if err != nil {
			return planProblemBytes(err)
		}
		return renderJSON(w, r, http.StatusCreated, toPlanDTO(plan))
	})
}

// handleGetPlan 读取 plan（含步骤执行结果）。
func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	plan, err := s.svc.Plan(r.Context(), r.PathValue("plan_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	wi, err := s.store.WorkItems().Get(r.Context(), plan.WorkItemID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := requireTaskWorkItemHTTP(wi); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPlanDTO(plan))
}

// handleWorkItemPlan 返回该主任务最新一份 plan（按 created_at 最新，不限状态）；
// 无 plan → 404 problem+json。
func (s *Server) handleWorkItemPlan(w http.ResponseWriter, r *http.Request) {
	wi, err := s.store.WorkItems().Get(r.Context(), r.PathValue("work_item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := requireTaskWorkItemHTTP(wi); err != nil {
		fail(w, r, err)
		return
	}
	plan, err := s.svc.LatestPlanForWorkItem(r.Context(), r.PathValue("work_item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPlanDTO(plan))
}

// handleWorkItemTree 先序返回以该任务为根的整棵子树（含根；DTO 带 parent_id）。
func (s *Server) handleWorkItemTree(w http.ResponseWriter, r *http.Request) {
	wi, err := s.store.WorkItems().Get(r.Context(), r.PathValue("work_item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := requireTaskWorkItemHTTP(wi); err != nil {
		fail(w, r, err)
		return
	}
	items, err := s.svc.WorkItemTree(r.Context(), r.PathValue("work_item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	dtos := make([]workItemDTO, 0, len(items))
	for _, wi := range items {
		dtos = append(dtos, s.enrichWorkItem(r, wi))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": dtos})
}
