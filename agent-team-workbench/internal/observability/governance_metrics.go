package observability

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

const (
	ErrorFamilySyntax    = "syntax"
	ErrorFamilySchema    = "schema"
	ErrorFamilySemantic  = "semantic"
	ErrorFamilyAuthority = "authority"
	ErrorFamilyQuota     = "quota"
	ErrorFamilyUnknown   = "unknown"
)

// GovernanceMetrics is a deterministic read model. Workspace counters are
// folded from canonical events; the application layer replaces GoalSummaries
// with a checked fold of authoritative Goal/receipt/quota/Run facts. It
// contains no process-local counters.
type GovernanceMetrics struct {
	WorkspaceID              string           `json:"workspace_id,omitempty"`
	SourceEventSeq           int64            `json:"source_event_seq"`
	PlanDecodeSuccess        int64            `json:"plan_decode_success"`
	PlanDecodeErrors         map[string]int64 `json:"plan_decode_errors"`
	RepairAttempts           int64            `json:"repair_attempts"`
	RepairSuccesses          int64            `json:"repair_successes"`
	RepairBlockers           int64            `json:"repair_blockers"`
	ReceiptReplays           int64            `json:"receipt_replays"`
	ReceiptConflicts         int64            `json:"receipt_conflicts"`
	ProjectionDivergences    int64            `json:"projection_divergences"`
	EvidenceFinishRejections int64            `json:"evidence_finish_rejections"`
	UserUnblocks             int64            `json:"user_unblocks"`
	ProjectionUpdates        int64            `json:"projection_updates"`
	Handoffs                 int64            `json:"handoffs"`
	EvidenceItems            int64            `json:"evidence_items"`
	GoalSummaries            []GoalMetrics    `json:"goal_summaries"`
}

// GoalMetrics is the per-Goal accounting view exposed with workspace
// governance metrics. Spend is counted only from committed per-Run quota
// entries; unresolved entries never become fabricated zero-value spend.
type GoalMetrics struct {
	GoalID              string `json:"goal_id"`
	TurnCount           int64  `json:"turn_count"`
	RunCount            int64  `json:"run_count"`
	InputTokensTotal    int64  `json:"input_tokens_total"`
	InputUncachedTokens int64  `json:"input_uncached_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	CacheWriteTokens    int64  `json:"cache_write_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	CostMicroUSD        int64  `json:"cost_microusd"`
}

func NewGovernanceMetrics() GovernanceMetrics {
	return GovernanceMetrics{PlanDecodeErrors: map[string]int64{}, GoalSummaries: []GoalMetrics{}}
}

// AggregateGovernanceMetrics folds canonical events in stream order. Event
// IDs are not used as mutable state; the only transient map tracks a repair
// checkpoint within this one deterministic fold so a repair-success event can
// be paired with the preceding repair attempt. Numeric spend aggregation is
// checked and returns a validation error rather than wrapping int64.
func AggregateGovernanceMetrics(events []*domain.CanonicalEvent) (GovernanceMetrics, error) {
	metrics := NewGovernanceMetrics()
	goalSummaryIndex := map[string]int{}
	goalTurnSeen := map[string]struct{}{}
	repairPending := map[string]bool{}
	evidenceCoordinatorKeys := map[string]struct{}{}
	evidenceWorkItemKeys := map[string]int64{}
	evidenceSeen := map[string]struct{}{}
	activeBlockCode := map[string]string{}
	for _, event := range events {
		if event == nil {
			continue
		}
		if event.StreamSeq > metrics.SourceEventSeq {
			metrics.SourceEventSeq = event.StreamSeq
		}
		data := event.Data
		switch event.Type {
		case domain.EventTurnReceiptAppended:
			if stringValue(data, "record_kind") == "header" && stringValue(data, "outcome") == "" {
				goalID := stringValue(data, "goal_id")
				if goalID != "" {
					index := ensureGoalMetrics(&metrics, goalSummaryIndex, goalID)
					turnKey := goalID + "\x00" + event.AggregateID + "\x00" + formatInteger(data["turn_seq"])
					if _, seen := goalTurnSeen[turnKey]; !seen {
						goalTurnSeen[turnKey] = struct{}{}
						metrics.GoalSummaries[index].TurnCount++
					}
				}
			}
			if stringValue(data, "outcome") == "replayed" {
				metrics.ReceiptReplays++
			}
			if stringValue(data, "outcome") == "conflict" {
				metrics.ReceiptConflicts++
			}
			if stringValue(data, "record_kind") == "phase" &&
				stringValue(data, "outcome") == "" &&
				stringValue(data, "phase") == string(domain.TurnReceiptPhaseDecisionDecode) {
				metrics.PlanDecodeSuccess++
			}
			if stringValue(data, "record_kind") == "phase" &&
				stringValue(data, "phase") == string(domain.TurnReceiptPhaseValidation) &&
				data["valid"] == false {
				incrementErrorFamily(&metrics, stringValue(data, "error_code"))
			}
		case domain.EventCoordinatorAttemptUpdated:
			if stringValue(data, "stage") == "repair" {
				if attempt, ok := integerValue(data["repair_attempt"]); ok && attempt > 0 {
					metrics.RepairAttempts++
				}
				repairPending[event.AggregateID] = true
				incrementErrorFamily(&metrics, stringValue(data, "plan_error_code"))
			}
		case domain.EventCoordinatorPlanUpdated:
			if stringValue(data, "stage") == "decision" && repairPending[event.AggregateID] {
				metrics.RepairSuccesses++
				delete(repairPending, event.AggregateID)
			}
		case domain.EventCoordinatorBlocked:
			failureCode := firstString(data, "failure_code", "blocker_code", "plan_error_code")
			incrementErrorFamily(&metrics, firstString(data, "plan_error_code"))
			if rootID := stringValue(data, "root_work_item_id"); rootID != "" {
				activeBlockCode[rootID] = failureCode
			}
			if repairPending[event.AggregateID] || stringValue(data, "stage") == "repair" || strings.Contains(strings.ToLower(failureCode), "repair") {
				metrics.RepairBlockers++
				delete(repairPending, event.AggregateID)
			}
			if isEvidenceFinishRejection(failureCode) {
				key := stringValue(data, "root_work_item_id") + "\x00" + failureCode
				evidenceCoordinatorKeys[key] = struct{}{}
			}
		case domain.EventWorkItemBlocked:
			code := firstString(data, "code", "failure_code", "blocker_code")
			activeBlockCode[event.AggregateID] = code
			if isEvidenceFinishRejection(code) {
				key := event.AggregateID + "\x00" + code
				evidenceWorkItemKeys[key]++
			}
		case domain.EventWorkItemUnblocked:
			if isFormatBlockCode(activeBlockCode[event.AggregateID]) {
				metrics.UserUnblocks++
			}
			delete(activeBlockCode, event.AggregateID)
		case domain.EventProjectionUpdated:
			metrics.ProjectionUpdates++
			cause := strings.ToLower(stringValue(data, "cause"))
			if cause == "consistency_issue" || strings.Contains(cause, "divergence") || data["inconsistent"] == true {
				metrics.ProjectionDivergences++
			}
		case domain.EventHandoffCreated:
			metrics.Handoffs++
		case domain.EventGoalEvidenceAdded:
			key := stringValue(data, "source_kind") + "\x00" + stringValue(data, "source_id")
			if key == "\x00" {
				key = event.EventID
			}
			if _, seen := evidenceSeen[key]; !seen {
				evidenceSeen[key] = struct{}{}
				metrics.EvidenceItems++
			}
		case domain.EventValidationRecorded:
			key := "validation_result\x00" + firstString(data, "validation_result_id", "source_run_id")
			if key == "validation_result\x00" {
				key = event.EventID
			}
			if _, seen := evidenceSeen[key]; !seen {
				evidenceSeen[key] = struct{}{}
				metrics.EvidenceItems++
			}
		case domain.EventQuotaSpendRecorded:
			if stringValue(data, "status") == string(domain.QuotaSpendCommitted) {
				goalID := stringValue(data, "goal_id")
				if goalID == "" {
					return metrics, fmt.Errorf("%w: committed quota spend event requires goal_id", domain.ErrValidation)
				}
				amount, ok := integerValue(data["amount"])
				if !ok || amount < 0 {
					return metrics, fmt.Errorf("%w: committed quota spend event has invalid amount", domain.ErrValidation)
				}
				index := ensureGoalMetrics(&metrics, goalSummaryIndex, goalID)
				if err := addGoalSpend(&metrics.GoalSummaries[index], stringValue(data, "quota_kind"), amount); err != nil {
					return metrics, err
				}
			}
		}
	}
	sort.Slice(metrics.GoalSummaries, func(i, j int) bool {
		return metrics.GoalSummaries[i].GoalID < metrics.GoalSummaries[j].GoalID
	})
	for key := range evidenceCoordinatorKeys {
		metrics.EvidenceFinishRejections++
		delete(evidenceWorkItemKeys, key)
	}
	for _, count := range evidenceWorkItemKeys {
		metrics.EvidenceFinishRejections += count
	}
	return metrics, nil
}

func ensureGoalMetrics(metrics *GovernanceMetrics, index map[string]int, goalID string) int {
	if existing, ok := index[goalID]; ok {
		return existing
	}
	index[goalID] = len(metrics.GoalSummaries)
	metrics.GoalSummaries = append(metrics.GoalSummaries, GoalMetrics{GoalID: goalID})
	return index[goalID]
}

func addGoalSpend(summary *GoalMetrics, kind string, amount int64) error {
	if summary == nil || amount < 0 {
		return fmt.Errorf("%w: invalid Goal metric spend amount", domain.ErrValidation)
	}
	add := func(current *int64) error {
		var err error
		*current, err = domain.CheckedAddNonNegative(*current, amount)
		return err
	}
	switch domain.QuotaKind(kind) {
	case domain.QuotaInputTokensTotal:
		return add(&summary.InputTokensTotal)
	case domain.QuotaInputUncachedTokens:
		return add(&summary.InputUncachedTokens)
	case domain.QuotaCacheReadTokens:
		return add(&summary.CacheReadTokens)
	case domain.QuotaCacheWriteTokens:
		return add(&summary.CacheWriteTokens)
	case domain.QuotaOutputTokens:
		return add(&summary.OutputTokens)
	case domain.QuotaCostMicroUSD:
		return add(&summary.CostMicroUSD)
	default:
		return fmt.Errorf("%w: quota spend event has invalid quota_kind %q", domain.ErrValidation, kind)
	}
}

func formatInteger(value any) string {
	if integer, ok := integerValue(value); ok {
		return strconv.FormatInt(integer, 10)
	}
	return "?"
}

func incrementErrorFamily(metrics *GovernanceMetrics, code string) {
	if metrics == nil || !isPlanErrorCode(code) {
		return
	}
	family := ErrorFamily(code)
	metrics.PlanDecodeErrors[family]++
}

func isPlanErrorCode(code string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	return strings.Contains(code, "plan_json") || strings.Contains(code, "plan_parse") ||
		strings.Contains(code, "plan_schema") || strings.Contains(code, "plan_semantic") ||
		strings.Contains(code, "plan_authority") || strings.Contains(code, "plan_quota")
}

func isFormatBlockCode(code string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	return strings.Contains(code, "plan_json") || strings.Contains(code, "plan_parse") ||
		strings.Contains(code, "plan_schema") || strings.Contains(code, "coordinator_plan_repair")
}

// ErrorFamily normalizes the persisted PlanDecision error vocabulary into the
// five contract families used by dashboards and reports.
func ErrorFamily(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	switch {
	case strings.Contains(code, "syntax"), strings.Contains(code, "json"):
		return ErrorFamilySyntax
	case strings.Contains(code, "schema"):
		return ErrorFamilySchema
	case strings.Contains(code, "semantic"):
		return ErrorFamilySemantic
	case strings.Contains(code, "authority"), strings.Contains(code, "scope"), strings.Contains(code, "capability"):
		return ErrorFamilyAuthority
	case strings.Contains(code, "quota"), strings.Contains(code, "budget"):
		return ErrorFamilyQuota
	default:
		return ErrorFamilyUnknown
	}
}

func isEvidenceFinishRejection(code string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	return strings.Contains(code, "evidence") &&
		(strings.Contains(code, "insufficient") || strings.Contains(code, "reject") || strings.Contains(code, "missing"))
}

func stringValue(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value, _ := data[key].(string)
	return value
}

func firstString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(data, key); value != "" {
			return value
		}
	}
	return ""
}

func integerValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed < 0 || typed >= float64(math.MaxInt64) || math.Trunc(typed) != typed {
			return 0, false
		}
		return int64(typed), true
	default:
		return 0, false
	}
}
