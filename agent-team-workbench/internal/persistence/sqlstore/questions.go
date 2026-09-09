package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// QuestionRepo is the SQLite implementation of native AskUserQuestion state.
// Provider identity is retained alongside the control-plane id so a replayed
// Kimi frame cannot resolve a question from another run or session.
type QuestionRepo struct{ store *Store }

const questionColumns = `id, run_id, work_item_id, session_ref, provider_id,
 agent_id, provider_turn, tool_call_id, questions_json, status, response_json, provider_answer_json,
 created_at, resolved_at`

const questionColumnsQualified = `q.id, q.run_id, q.work_item_id, q.session_ref, q.provider_id,
 q.agent_id, q.provider_turn, q.tool_call_id, q.questions_json, q.status, q.response_json, q.provider_answer_json,
 q.created_at, q.resolved_at`

func (r *QuestionRepo) Create(ctx context.Context, q *domain.QuestionRequest) error {
	if err := q.Validate(); err != nil {
		return fmt.Errorf("validate question: %w", err)
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO questions(`+questionColumns+`)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		q.ID, q.RunID, q.WorkItemID, q.SessionRef, q.ProviderID,
		nullString(q.AgentID), q.TurnID, nullString(q.ToolCallID), jsonText(q.Questions),
		q.Status, nullString(jsonTextOrEmpty(q.Response)), nullString(jsonTextOrEmpty(q.ProviderAnswers)), timeParam(q.CreatedAt), nullTimeParam(q.ResolvedAt))
	return r.store.mapErr(err)
}

func jsonTextOrEmpty(value any) string {
	if value == nil {
		return ""
	}
	if response, ok := value.(*domain.QuestionResponse); ok && response == nil {
		return ""
	}
	return jsonText(value)
}

func (r *QuestionRepo) Get(ctx context.Context, id string) (*domain.QuestionRequest, error) {
	q := &domain.QuestionRequest{}
	if err := r.scan(r.store.queryRow(ctx, r.store.exec(ctx), `SELECT `+questionColumns+` FROM questions WHERE id=?`, id), q); err != nil {
		return nil, r.store.mapErr(err)
	}
	return q, nil
}

func (r *QuestionRepo) GetByProviderKey(ctx context.Context, runID, sessionRef, providerID string) (*domain.QuestionRequest, error) {
	q := &domain.QuestionRequest{}
	if err := r.scan(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+questionColumns+` FROM questions WHERE run_id=? AND session_ref=? AND provider_id=?`,
		runID, sessionRef, providerID), q); err != nil {
		return nil, r.store.mapErr(err)
	}
	return q, nil
}

func (r *QuestionRepo) ListPending(ctx context.Context, runID string) ([]*domain.QuestionRequest, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+questionColumnsQualified+` FROM questions q
         JOIN execution_runs r ON r.id=q.run_id
         WHERE q.run_id=? AND q.status=?
           AND r.status NOT IN ('succeeded','interrupted','cancelled','lost','failed')
         ORDER BY q.created_at`, runID, domain.QuestionPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.QuestionRequest
	for rows.Next() {
		q := &domain.QuestionRequest{}
		if err := r.scan(rows, q); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (r *QuestionRepo) Update(ctx context.Context, q *domain.QuestionRequest) error {
	if q == nil || q.ID == "" {
		return fmt.Errorf("question id required")
	}
	result, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE questions SET status=?, response_json=?, provider_answer_json=?, resolved_at=? WHERE id=?`,
		q.Status, nullString(jsonTextOrEmpty(q.Response)), nullString(jsonTextOrEmpty(q.ProviderAnswers)), nullTimeParam(q.ResolvedAt), q.ID)
	if err != nil {
		return r.store.mapErr(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

type questionScanner interface{ Scan(...any) error }

func (r *QuestionRepo) scan(row questionScanner, q *domain.QuestionRequest) error {
	var agentID, toolCallID, responseJSON, providerAnswerJSON *string
	var turnID int64
	var created, resolved scanTime
	var questionsJSON string
	if err := row.Scan(&q.ID, &q.RunID, &q.WorkItemID, &q.SessionRef, &q.ProviderID,
		&agentID, &turnID, &toolCallID, &questionsJSON, &q.Status, &responseJSON, &providerAnswerJSON, &created, &resolved); err != nil {
		return err
	}
	q.TurnID = turnID
	q.AgentID = valueString(agentID)
	q.ToolCallID = valueString(toolCallID)
	if err := jsonInto(questionsJSON, &q.Questions); err != nil {
		return fmt.Errorf("decode question items: %w", err)
	}
	if responseJSON != nil && *responseJSON != "" {
		q.Response = &domain.QuestionResponse{}
		if err := jsonInto(*responseJSON, q.Response); err != nil {
			return fmt.Errorf("decode question response: %w", err)
		}
	}
	if providerAnswerJSON != nil && *providerAnswerJSON != "" && *providerAnswerJSON != "null" {
		if err := jsonInto(*providerAnswerJSON, &q.ProviderAnswers); err != nil {
			return fmt.Errorf("decode provider answer: %w", err)
		}
	}
	q.CreatedAt = mustTime(created)
	q.ResolvedAt = optTime(resolved)
	return nil
}

func valueString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

var _ = time.Time{}
