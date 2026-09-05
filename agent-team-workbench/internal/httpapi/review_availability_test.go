package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func reviewFromWorkItemResponse(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	review, ok := body["review"].(map[string]any)
	if !ok {
		t.Fatalf("WorkItem 响应缺少 review 读模型：%v", body)
	}
	return review
}

func TestWorkItemReviewAvailabilityIsConsistentAcrossReads(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID := seedCommentsHTTPEnv(t, s)
	ctx := context.Background()
	root, err := s.svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "只读验收投影"})
	if err != nil {
		t.Fatal(err)
	}
	pinHTTPReviewPhase(t, s, ctx, root.ID, time.Now().UTC())

	code, detail := getJSON(t, mux, "/api/v1/work-items/"+root.ID)
	if code != http.StatusOK {
		t.Fatalf("WorkItem 详情应 200：%d %v", code, detail)
	}
	detailReview := reviewFromWorkItemResponse(t, detail)
	if detailReview["ready"] != true || detailReview["can_accept"] != true || detailReview["can_return"] != true {
		t.Fatalf("owner 的 legacy review 投影应可操作：%v", detailReview)
	}

	code, list := getJSON(t, mux, "/api/v1/workspaces/"+wsID+"/work-items?record_kind=task")
	if code != http.StatusOK {
		t.Fatalf("WorkItem 列表应 200：%d %v", code, list)
	}
	listItems, _ := list["items"].([]any)
	var listReview map[string]any
	for _, raw := range listItems {
		item, _ := raw.(map[string]any)
		if item["id"] == root.ID {
			listReview = reviewFromWorkItemResponse(t, item)
			break
		}
	}
	if listReview == nil || listReview["ready"] != detailReview["ready"] ||
		listReview["can_accept"] != detailReview["can_accept"] || listReview["can_return"] != detailReview["can_return"] {
		t.Fatalf("列表与详情 review 投影不一致：详情=%v 列表=%v", detailReview, listReview)
	}

	code, bootstrap := getJSON(t, mux, "/api/v1/workspaces/"+wsID+"/bootstrap")
	if code != http.StatusOK {
		t.Fatalf("bootstrap 应 200：%d %v", code, bootstrap)
	}
	workItems, _ := bootstrap["work_items"].(map[string]any)
	bootstrapItems, _ := workItems["items"].([]any)
	var bootstrapReview map[string]any
	for _, raw := range bootstrapItems {
		item, _ := raw.(map[string]any)
		if item["id"] == root.ID {
			bootstrapReview = reviewFromWorkItemResponse(t, item)
			break
		}
	}
	if bootstrapReview == nil || bootstrapReview["ready"] != detailReview["ready"] ||
		bootstrapReview["can_accept"] != detailReview["can_accept"] || bootstrapReview["can_return"] != detailReview["can_return"] {
		t.Fatalf("bootstrap 与详情 review 投影不一致：详情=%v bootstrap=%v", detailReview, bootstrapReview)
	}

	// Readiness stays the same when the actor loses approval permission; only
	// the action capability changes in the same read model.
	s.SetDemoRole(domain.RoleOperator)
	code, operatorDetail := getJSON(t, mux, "/api/v1/work-items/"+root.ID)
	if code != http.StatusOK {
		t.Fatalf("operator 读取详情应 200：%d %v", code, operatorDetail)
	}
	operatorReview := reviewFromWorkItemResponse(t, operatorDetail)
	if operatorReview["ready"] != true || operatorReview["can_accept"] != false || operatorReview["can_return"] != true ||
		operatorReview["accept_reason"] != "当前角色无权验收任务" {
		t.Fatalf("权限只应影响验收动作：%v", operatorReview)
	}
	code, me := getJSON(t, mux, "/api/v1/me")
	if code != http.StatusOK || me["role"] != "operator" {
		t.Fatalf("/me 与 review 的当前角色应一致：%d %v", code, me)
	}
}
