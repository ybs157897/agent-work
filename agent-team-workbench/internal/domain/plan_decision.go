package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type PlanDecisionV2 struct {
	SchemaVersion string               `json:"schema_version"`
	Kind          string               `json:"kind"`
	Reason        string               `json:"reason"`
	NextAction    string               `json:"next_action"`
	Steps         []PlanDecisionStepV2 `json:"steps"`
}

type PlanDecisionStepV2 struct {
	Verb             PlanVerb                    `json:"-"`
	Dispatch         *PlanDispatchStepV2         `json:"-"`
	ConsultKnowledge *PlanConsultKnowledgeStepV2 `json:"-"`
	Defer            *PlanDeferStepV2            `json:"-"`
	Join             *PlanJoinStepV2             `json:"-"`
	Finish           *PlanFinishStepV2           `json:"-"`
}

type PlanDispatchStepV2 struct {
	AgentID       string    `json:"agent_id"`
	Title         string    `json:"title"`
	Instruction   string    `json:"instruction"`
	Acceptance    []string  `json:"acceptance"`
	Priority      *Priority `json:"priority,omitempty"`
	KnowledgeFrom *int      `json:"knowledge_from,omitempty"`
}

type PlanConsultKnowledgeStepV2 struct {
	Corpus string   `json:"corpus"`
	Terms  []string `json:"terms"`
	Limit  *int     `json:"limit,omitempty"`
}

type PlanDeferStepV2 struct {
	WakeAt *RFC3339Time `json:"wake_at,omitempty"`
}

type PlanJoinStepV2 struct {
	Children JoinChildren `json:"children"`
	WakeAt   *RFC3339Time `json:"wake_at,omitempty"`
}

type PlanFinishStepV2 struct {
	Evaluation *bool `json:"evaluation,omitempty"`
}

type RFC3339Time struct{ time.Time }

func (t *RFC3339Time) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("RFC3339 time must be a JSON string: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return fmt.Errorf("invalid RFC3339 time: %w", err)
	}
	t.Time = parsed.UTC()
	return nil
}

func (t RFC3339Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.UTC().Format(time.RFC3339Nano))
}

func (t RFC3339Time) String() string { return t.UTC().Format(time.RFC3339Nano) }

type JoinChildren struct {
	All bool
	IDs []string
}

func (c *JoinChildren) UnmarshalJSON(raw []byte) error {
	var all string
	if err := json.Unmarshal(raw, &all); err == nil {
		if all != "all" {
			return fmt.Errorf("join children string must be all")
		}
		c.All, c.IDs = true, nil
		return nil
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return fmt.Errorf("join children must be all or a string array: %w", err)
	}
	if len(ids) == 0 {
		return fmt.Errorf("join children array must not be empty")
	}
	c.All, c.IDs = false, append([]string(nil), ids...)
	return nil
}

func (c JoinChildren) MarshalJSON() ([]byte, error) {
	if c.All {
		return json.Marshal("all")
	}
	return json.Marshal(c.IDs)
}

type planDecisionWire struct {
	SchemaVersion string               `json:"schema_version"`
	Kind          string               `json:"kind"`
	Reason        string               `json:"reason"`
	NextAction    string               `json:"next_action"`
	Steps         []PlanDecisionStepV2 `json:"steps"`
}

func (d *PlanDecisionV2) UnmarshalJSON(raw []byte) error {
	var wire planDecisionWire
	if err := decodeStrict(raw, &wire); err != nil {
		return err
	}
	d.SchemaVersion, d.Kind, d.Reason, d.NextAction = wire.SchemaVersion, wire.Kind, wire.Reason, wire.NextAction
	d.Steps = wire.Steps
	return nil
}

func (d PlanDecisionV2) MarshalJSON() ([]byte, error) {
	return json.Marshal(planDecisionWire{
		SchemaVersion: d.SchemaVersion, Kind: d.Kind, Reason: d.Reason, NextAction: d.NextAction, Steps: d.Steps,
	})
}

type planStepVerbProbe struct {
	Verb PlanVerb `json:"verb"`
}

type dispatchStepWire struct {
	Verb PlanVerb `json:"verb"`
	PlanDispatchStepV2
}

type consultKnowledgeStepWire struct {
	Verb PlanVerb `json:"verb"`
	PlanConsultKnowledgeStepV2
}

type deferStepWire struct {
	Verb PlanVerb `json:"verb"`
	PlanDeferStepV2
}

type joinStepWire struct {
	Verb PlanVerb `json:"verb"`
	PlanJoinStepV2
}

type finishStepWire struct {
	Verb PlanVerb `json:"verb"`
	PlanFinishStepV2
}

func (s *PlanDecisionStepV2) UnmarshalJSON(raw []byte) error {
	var probe planStepVerbProbe
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}
	*s = PlanDecisionStepV2{Verb: probe.Verb}
	switch probe.Verb {
	case PlanVerbDispatch:
		var wire dispatchStepWire
		if err := decodeStrict(raw, &wire); err != nil {
			return err
		}
		s.Dispatch = &wire.PlanDispatchStepV2
	case PlanVerbConsultKnowledge:
		var wire consultKnowledgeStepWire
		if err := decodeStrict(raw, &wire); err != nil {
			return err
		}
		s.ConsultKnowledge = &wire.PlanConsultKnowledgeStepV2
	case PlanVerbDefer:
		var wire deferStepWire
		if err := decodeStrict(raw, &wire); err != nil {
			return err
		}
		s.Defer = &wire.PlanDeferStepV2
	case PlanVerbJoin:
		var wire joinStepWire
		if err := decodeStrict(raw, &wire); err != nil {
			return err
		}
		s.Join = &wire.PlanJoinStepV2
	case PlanVerbFinish:
		var wire finishStepWire
		if err := decodeStrict(raw, &wire); err != nil {
			return err
		}
		s.Finish = &wire.PlanFinishStepV2
	default:
		return fmt.Errorf("unsupported plan verb %q", probe.Verb)
	}
	return nil
}

func (s PlanDecisionStepV2) MarshalJSON() ([]byte, error) {
	switch s.Verb {
	case PlanVerbDispatch:
		if s.Dispatch == nil {
			return nil, fmt.Errorf("dispatch branch missing")
		}
		return json.Marshal(dispatchStepWire{Verb: s.Verb, PlanDispatchStepV2: *s.Dispatch})
	case PlanVerbConsultKnowledge:
		if s.ConsultKnowledge == nil {
			return nil, fmt.Errorf("consult_knowledge branch missing")
		}
		return json.Marshal(consultKnowledgeStepWire{Verb: s.Verb, PlanConsultKnowledgeStepV2: *s.ConsultKnowledge})
	case PlanVerbDefer:
		if s.Defer == nil {
			return nil, fmt.Errorf("defer branch missing")
		}
		return json.Marshal(deferStepWire{Verb: s.Verb, PlanDeferStepV2: *s.Defer})
	case PlanVerbJoin:
		if s.Join == nil {
			return nil, fmt.Errorf("join branch missing")
		}
		return json.Marshal(joinStepWire{Verb: s.Verb, PlanJoinStepV2: *s.Join})
	case PlanVerbFinish:
		if s.Finish == nil {
			return nil, fmt.Errorf("finish branch missing")
		}
		return json.Marshal(finishStepWire{Verb: s.Verb, PlanFinishStepV2: *s.Finish})
	default:
		return nil, fmt.Errorf("unsupported plan verb %q", s.Verb)
	}
}

func decodeStrict(raw []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}
