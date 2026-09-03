package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	workbenchcontracts "github.com/ybs/agent-team-workbench/contracts"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

const (
	planDecisionSchemaResourceID = "https://workbench.example/contracts/control/plan-decision-v2.schema.json"
	maxPlanDecisionBytes         = 2 << 20
	planDecisionSchemaVersion    = "plan-decision/v2"
	planDecisionTransportSchema  = "provider_schema"
	planDecisionTransportTool    = "control_tool"
	planDecisionTransportText    = "text_decoder"
)

func plannerCoordinatorContext(contextData map[string]any) bool {
	if contextData == nil {
		return false
	}
	role, _ := contextData["role"].(string)
	action, _ := contextData["action"].(string)
	return role == coordinatorRole && action != "evaluation"
}

func plannerCapabilityLevel(binding *domain.RuntimeBinding, name string) string {
	if binding == nil || binding.Capabilities == nil {
		return string(runtime.CapUnavailable)
	}
	level := runtime.CapabilityLevel(binding.Capabilities[name])
	switch level {
	case runtime.CapSupported, runtime.CapExperimental, runtime.CapAdapterTranslated, runtime.CapUnavailable:
		return string(level)
	default:
		return string(runtime.CapUnavailable)
	}
}

func planDecisionControlSnapshot(binding *domain.RuntimeBinding, contextData map[string]any) map[string]any {
	capabilities := map[string]any{
		runtime.CapabilityStructuredTransport:     plannerCapabilityLevel(binding, runtime.CapabilityStructuredTransport),
		runtime.CapabilitySchemaConstrainedOutput: plannerCapabilityLevel(binding, runtime.CapabilitySchemaConstrainedOutput),
		runtime.CapabilityControlToolCall:         plannerCapabilityLevel(binding, runtime.CapabilityControlToolCall),
	}
	transportMode := planDecisionTransportText
	if capabilities[runtime.CapabilitySchemaConstrainedOutput] == string(runtime.CapSupported) {
		transportMode = planDecisionTransportSchema
	} else if capabilities[runtime.CapabilityControlToolCall] == string(runtime.CapSupported) {
		transportMode = planDecisionTransportTool
	}
	repairAttempt := 0
	if repair, ok := contextData["repair"].(map[string]any); ok {
		repairAttempt = coordinatorAttemptValue(repair["repair_attempt"])
	}
	return map[string]any{
		"schema_version": planDecisionSchemaVersion,
		"schema_digest":  workbenchcontracts.PlanDecisionV2SchemaDigest(),
		"transport_mode": transportMode,
		"capabilities":   capabilities,
		"repair_attempt": repairAttempt,
	}
}

type PlanDecisionError struct {
	Code    domain.GovernanceErrorCode
	Path    string
	Message string
	Cause   error
}

func (e *PlanDecisionError) Error() string {
	if e == nil {
		return ""
	}
	if e.Path != "" {
		return fmt.Sprintf("%s at %s: %s", e.Code, e.Path, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *PlanDecisionError) Unwrap() error { return e.Cause }

var compiledPlanDecisionSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(workbenchcontracts.PlanDecisionV2Schema()))
	if err != nil {
		return nil, fmt.Errorf("parse embedded PlanDecisionV2 schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	if err := compiler.AddResource(planDecisionSchemaResourceID, doc); err != nil {
		return nil, fmt.Errorf("register PlanDecisionV2 schema: %w", err)
	}
	compiled, err := compiler.Compile(planDecisionSchemaResourceID)
	if err != nil {
		return nil, fmt.Errorf("compile PlanDecisionV2 schema: %w", err)
	}
	return compiled, nil
})

func DecodePlanDecisionV2(raw []byte) (*domain.PlanDecisionV2, error) {
	if len(raw) == 0 || len(raw) > maxPlanDecisionBytes || !utf8.Valid(raw) {
		return nil, &PlanDecisionError{
			Code: domain.GovernanceErrorPlanJSONSyntax, Path: "/",
			Message: fmt.Sprintf("decision must be valid UTF-8 JSON within 1..%d bytes", maxPlanDecisionBytes),
		}
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, &PlanDecisionError{
			Code: domain.GovernanceErrorPlanJSONSyntax, Path: "/", Message: err.Error(), Cause: err,
		}
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, &PlanDecisionError{
			Code: domain.GovernanceErrorPlanJSONSyntax, Path: "/", Message: err.Error(), Cause: err,
		}
	}
	schema, err := compiledPlanDecisionSchema()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(instance); err != nil {
		return nil, planSchemaError(err)
	}
	var decision domain.PlanDecisionV2
	if err := json.Unmarshal(raw, &decision); err != nil {
		return nil, &PlanDecisionError{
			Code: domain.GovernanceErrorPlanSchemaValidation, Path: "/",
			Message: "typed decode failed after schema validation: " + err.Error(), Cause: err,
		}
	}
	if err := validatePlanDecisionSemantics(&decision); err != nil {
		return nil, err
	}
	return &decision, nil
}

func planSchemaError(err error) error {
	path := "/"
	var validation *jsonschema.ValidationError
	if errors.As(err, &validation) && len(validation.InstanceLocation) > 0 {
		parts := make([]string, 0, len(validation.InstanceLocation))
		for _, part := range validation.InstanceLocation {
			parts = append(parts, strings.ReplaceAll(strings.ReplaceAll(part, "~", "~0"), "/", "~1"))
		}
		path += strings.Join(parts, "/")
	}
	return &PlanDecisionError{
		Code: domain.GovernanceErrorPlanSchemaValidation, Path: path, Message: err.Error(), Cause: err,
	}
}

func validatePlanDecisionSemantics(decision *domain.PlanDecisionV2) error {
	if decision == nil {
		return semanticPlanError("/", "decision required")
	}
	dispatchAwaitingBarrier := false
	for index := range decision.Steps {
		step := &decision.Steps[index]
		path := fmt.Sprintf("/steps/%d", index)
		switch step.Verb {
		case domain.PlanVerbConsultKnowledge:
		case domain.PlanVerbDispatch:
			if step.Dispatch == nil {
				return semanticPlanError(path, "dispatch branch missing")
			}
			if step.Dispatch.KnowledgeFrom != nil {
				ref := *step.Dispatch.KnowledgeFrom
				if ref < 0 || ref >= index || decision.Steps[ref].Verb != domain.PlanVerbConsultKnowledge {
					return semanticPlanError(path+"/knowledge_from", "must reference an earlier consult_knowledge step")
				}
			}
			dispatchAwaitingBarrier = true
		case domain.PlanVerbDefer, domain.PlanVerbJoin:
			dispatchAwaitingBarrier = false
			if index != len(decision.Steps)-1 {
				return semanticPlanError(path, "join/defer barrier must be the final step")
			}
		case domain.PlanVerbFinish:
			if dispatchAwaitingBarrier {
				return semanticPlanError(path, "finish cannot bypass a join/defer barrier after dispatch")
			}
			if index != len(decision.Steps)-1 {
				return semanticPlanError(path, "finish must be the final step")
			}
		default:
			return semanticPlanError(path+"/verb", "unsupported verb")
		}
	}
	if dispatchAwaitingBarrier {
		return semanticPlanError("/steps", "dispatch decision must end with join or defer")
	}
	return nil
}

func semanticPlanError(path, message string) error {
	return &PlanDecisionError{Code: domain.GovernanceErrorPlanSemanticValidation, Path: path, Message: message}
}

func PlanDecisionStepInputs(decision *domain.PlanDecisionV2) ([]PlanStepInput, error) {
	if err := validatePlanDecisionSemantics(decision); err != nil {
		return nil, err
	}
	steps := make([]PlanStepInput, 0, len(decision.Steps))
	for _, step := range decision.Steps {
		payload := map[string]any{}
		switch step.Verb {
		case domain.PlanVerbDispatch:
			value := step.Dispatch
			payload["agent_id"], payload["title"], payload["instruction"] = value.AgentID, value.Title, value.Instruction
			payload["acceptance"] = stringsToAny(value.Acceptance)
			if value.Priority != nil {
				payload["priority"] = string(*value.Priority)
			}
			if value.KnowledgeFrom != nil {
				payload["knowledge_from"] = *value.KnowledgeFrom
			}
		case domain.PlanVerbConsultKnowledge:
			value := step.ConsultKnowledge
			payload["corpus"], payload["terms"] = value.Corpus, stringsToAny(value.Terms)
			if value.Limit != nil {
				payload["limit"] = *value.Limit
			}
		case domain.PlanVerbDefer:
			if step.Defer.WakeAt != nil {
				payload["wake_at"] = step.Defer.WakeAt.String()
			}
		case domain.PlanVerbJoin:
			if step.Join.Children.All {
				payload["children"] = "all"
			} else {
				payload["children"] = stringsToAny(step.Join.Children.IDs)
			}
			if step.Join.WakeAt != nil {
				payload["wake_at"] = step.Join.WakeAt.String()
			}
		case domain.PlanVerbFinish:
			if step.Finish.Evaluation != nil {
				payload["evaluation"] = *step.Finish.Evaluation
			}
		}
		steps = append(steps, PlanStepInput{Verb: string(step.Verb), Payload: payload})
	}
	return steps, nil
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for index, value := range values {
		out[index] = value
	}
	return out
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanUniqueJSONValue(decoder, ""); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}

func scanUniqueJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string at %s", path)
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate object key %q at %s", key, jsonPointer(path, key))
			}
			seen[key] = struct{}{}
			if err := scanUniqueJSONValue(decoder, jsonPointer(path, key)); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		index := 0
		for decoder.More() {
			if err := scanUniqueJSONValue(decoder, jsonPointer(path, fmt.Sprint(index))); err != nil {
				return err
			}
			index++
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func jsonPointer(base, part string) string {
	part = strings.ReplaceAll(strings.ReplaceAll(part, "~", "~0"), "/", "~1")
	return base + "/" + part
}

type PlanCandidateSource string

const (
	PlanCandidateNativeText PlanCandidateSource = "native_text"
)

func DecodeCoordinatorPlanText(text string) (*domain.PlanDecisionV2, PlanCandidateSource, bool, error) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return nil, "", false, nil
	}
	decision, err := DecodePlanDecisionV2([]byte(trimmed))
	return decision, PlanCandidateNativeText, true, err
}
