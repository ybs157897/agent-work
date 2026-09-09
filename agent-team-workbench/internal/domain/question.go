package domain

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

// QuestionStatus is the durable lifecycle of one native AskUserQuestion
// interaction. It is deliberately separate from ApprovalStatus: a question
// never becomes an approval or a steering input.
type QuestionStatus string

const (
	QuestionPending   QuestionStatus = "pending"
	QuestionAnswered  QuestionStatus = "answered"
	QuestionDismissed QuestionStatus = "dismissed"
	QuestionExpired   QuestionStatus = "expired"
)

type QuestionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type QuestionItem struct {
	ID               string           `json:"id"`
	Question         string           `json:"question"`
	Header           string           `json:"header,omitempty"`
	Body             string           `json:"body,omitempty"`
	Options          []QuestionOption `json:"options"`
	MultiSelect      bool             `json:"multi_select,omitempty"`
	AllowOther       bool             `json:"allow_other,omitempty"`
	OtherLabel       string           `json:"other_label,omitempty"`
	OtherDescription string           `json:"other_description,omitempty"`
}

type QuestionAnswer struct {
	Kind      string   `json:"kind"`
	OptionID  string   `json:"option_id,omitempty"`
	OptionIDs []string `json:"option_ids,omitempty"`
	Text      string   `json:"text,omitempty"`
	OtherText string   `json:"other_text,omitempty"`
}

func (a QuestionAnswer) Validate() error {
	switch a.Kind {
	case "single":
		if strings.TrimSpace(a.OptionID) == "" {
			return fmt.Errorf("single answer requires option_id")
		}
	case "multi":
		if len(a.OptionIDs) == 0 {
			return fmt.Errorf("multi answer requires option_ids")
		}
	case "other":
		if a.Text == "" {
			return fmt.Errorf("other answer requires text")
		}
	case "multi_with_other":
		if a.OtherText == "" {
			return fmt.Errorf("multi_with_other answer requires other_text")
		}
	case "skipped":
	default:
		return fmt.Errorf("unsupported answer kind %q", a.Kind)
	}
	return nil
}

type QuestionResponse struct {
	Answers map[string]QuestionAnswer `json:"answers"`
	Method  string                    `json:"method,omitempty"`
	Note    string                    `json:"note,omitempty"`
}

func (r QuestionResponse) Equal(other QuestionResponse) bool {
	return reflect.DeepEqual(r.Answers, other.Answers)
}

func (r QuestionResponse) Validate(request *QuestionRequest) error {
	if len(r.Answers) == 0 {
		return fmt.Errorf("answers is required")
	}
	if r.Method != "" && r.Method != "enter" && r.Method != "space" && r.Method != "number_key" && r.Method != "click" {
		return fmt.Errorf("invalid answer method %q", r.Method)
	}
	allowed := map[string]QuestionItem{}
	if request != nil {
		for _, item := range request.Questions {
			allowed[item.ID] = item
		}
	}
	for id, answer := range r.Answers {
		if request != nil {
			item, ok := allowed[id]
			if !ok {
				return fmt.Errorf("answer references unknown question %q", id)
			}
			if err := answer.Validate(); err != nil {
				return fmt.Errorf("question %s: %w", id, err)
			}
			if answer.Kind == "single" && item.MultiSelect {
				return fmt.Errorf("question %s is multi-select", id)
			}
			if (answer.Kind == "multi" || answer.Kind == "multi_with_other") && !item.MultiSelect {
				return fmt.Errorf("question %s is single-select", id)
			}
			optionIDs := map[string]struct{}{}
			for _, option := range item.Options {
				optionIDs[option.ID] = struct{}{}
			}
			if answer.OptionID != "" {
				if _, ok := optionIDs[answer.OptionID]; !ok {
					return fmt.Errorf("question %s references unknown option %q", id, answer.OptionID)
				}
			}
			for _, optionID := range answer.OptionIDs {
				if _, ok := optionIDs[optionID]; !ok {
					return fmt.Errorf("question %s references unknown option %q", id, optionID)
				}
			}
			if (answer.Kind == "other" || answer.Kind == "multi_with_other") && !item.AllowOther {
				return fmt.Errorf("question %s does not allow other text", id)
			}
		}
	}
	return nil
}

// QuestionRequest is the durable form of one provider interaction. ProviderID
// is the Kimi question_id used on the adapter REST endpoint; ID is the control
// plane identity used by the browser and is unique to a run/session pair.
type QuestionRequest struct {
	ID              string
	RunID           string
	WorkItemID      string
	SessionRef      string
	ProviderID      string
	AgentID         string
	TurnID          int64
	ToolCallID      string
	Questions       []QuestionItem
	Status          QuestionStatus
	Response        *QuestionResponse
	ProviderAnswers map[string]any
	CreatedAt       time.Time
	ResolvedAt      *time.Time
}

func (q *QuestionRequest) Validate() error {
	if q == nil || strings.TrimSpace(q.ID) == "" || strings.TrimSpace(q.RunID) == "" ||
		strings.TrimSpace(q.WorkItemID) == "" || strings.TrimSpace(q.SessionRef) == "" ||
		strings.TrimSpace(q.ProviderID) == "" || len(q.Questions) == 0 || len(q.Questions) > 4 {
		return ErrValidation
	}
	seenQuestions := map[string]struct{}{}
	for _, item := range q.Questions {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Question) == "" || len(item.Options) < 2 || len(item.Options) > 4 {
			return ErrValidation
		}
		if _, ok := seenQuestions[item.ID]; ok {
			return ErrValidation
		}
		seenQuestions[item.ID] = struct{}{}
		seenOptions := map[string]struct{}{}
		for _, option := range item.Options {
			if strings.TrimSpace(option.ID) == "" || strings.TrimSpace(option.Label) == "" {
				return ErrValidation
			}
			if _, ok := seenOptions[option.ID]; ok {
				return ErrValidation
			}
			seenOptions[option.ID] = struct{}{}
		}
	}
	if q.Status == "" {
		q.Status = QuestionPending
	}
	if q.Status != QuestionPending && q.Status != QuestionAnswered && q.Status != QuestionDismissed && q.Status != QuestionExpired {
		return ErrValidation
	}
	return nil
}

func (q *QuestionRequest) Resolve(response QuestionResponse, now time.Time) error {
	if q.Status == QuestionAnswered {
		return nil
	}
	if q.Status != QuestionPending {
		return &TransitionError{Entity: "question", From: string(q.Status), To: string(QuestionAnswered)}
	}
	if err := response.Validate(q); err != nil {
		return err
	}
	q.Status = QuestionAnswered
	q.Response = &response
	q.ResolvedAt = &now
	return nil
}

// PrepareResponse durably records the exact typed response while the provider
// delivery is still in flight. Status stays pending so a failed delivery never
// appears answered and can be retried after reconnect/recovery.
func (q *QuestionRequest) PrepareResponse(response QuestionResponse) error {
	if q.Status != QuestionPending {
		return &TransitionError{Entity: "question", From: string(q.Status), To: string(QuestionAnswered)}
	}
	if err := response.Validate(q); err != nil {
		return err
	}
	if q.Response != nil {
		if q.Response.Equal(response) {
			return nil
		}
		return fmt.Errorf("%w: another response is already being delivered", ErrStateConflict)
	}
	q.Response = &response
	return nil
}

// CompleteResponse is called only after the adapter confirms the provider REST
// answer was accepted.
func (q *QuestionRequest) CompleteResponse(now time.Time) error {
	if q.Status == QuestionAnswered {
		return nil
	}
	if q.Status != QuestionPending || q.Response == nil {
		return &TransitionError{Entity: "question", From: string(q.Status), To: string(QuestionAnswered)}
	}
	q.Status = QuestionAnswered
	q.ResolvedAt = &now
	return nil
}

func (q *QuestionRequest) MarkProviderAnswered(now time.Time, answers map[string]any) error {
	if q.Status == QuestionAnswered {
		q.ProviderAnswers = answers
		return nil
	}
	if q.Status != QuestionPending {
		return &TransitionError{Entity: "question", From: string(q.Status), To: string(QuestionAnswered)}
	}
	q.Status = QuestionAnswered
	q.ProviderAnswers = answers
	q.ResolvedAt = &now
	return nil
}

func (q *QuestionRequest) ClearPreparedResponse() {
	if q.Status == QuestionPending {
		q.Response = nil
	}
}

// MatchesProviderAnswers compares Kimi's flattened native answer record with
// the immutable typed response we prepared. A nil typed response means an
// external client resolved the provider interaction; the native event itself
// is then the authority.
func (q *QuestionRequest) MatchesProviderAnswers(answers map[string]any) bool {
	if q.Response == nil {
		return true
	}
	items := make(map[string]QuestionItem, len(q.Questions))
	for _, item := range q.Questions {
		items[item.ID] = item
	}
	for id, answer := range q.Response.Answers {
		item, ok := items[id]
		if !ok {
			return false
		}
		if answer.Kind == "skipped" {
			continue
		}
		key := item.Question
		value, ok := answers[key]
		if !ok {
			return false
		}
		want := ""
		label := func(optionID string) string {
			for _, option := range item.Options {
				if option.ID == optionID {
					return option.Label
				}
			}
			return optionID
		}
		switch answer.Kind {
		case "single":
			want = label(answer.OptionID)
		case "multi":
			for index, optionID := range answer.OptionIDs {
				if index > 0 {
					want += ", "
				}
				want += label(optionID)
			}
		case "other":
			want = answer.Text
		case "multi_with_other":
			for index, optionID := range answer.OptionIDs {
				if index > 0 {
					want += ", "
				}
				want += label(optionID)
			}
			if want != "" {
				want += ", "
			}
			want += answer.OtherText
		default:
			return false
		}
		if fmt.Sprint(value) != want {
			return false
		}
	}
	return true
}

func (q *QuestionRequest) Dismiss(now time.Time) error {
	if q.Status == QuestionDismissed {
		return nil
	}
	if q.Status != QuestionPending {
		return &TransitionError{Entity: "question", From: string(q.Status), To: string(QuestionDismissed)}
	}
	q.Status = QuestionDismissed
	q.Response = nil
	q.ResolvedAt = &now
	return nil
}

func (q *QuestionRequest) Expire(now time.Time) error {
	if q.Status == QuestionExpired {
		return nil
	}
	if q.Status != QuestionPending {
		return &TransitionError{Entity: "question", From: string(q.Status), To: string(QuestionExpired)}
	}
	q.Status = QuestionExpired
	q.ResolvedAt = &now
	return nil
}
