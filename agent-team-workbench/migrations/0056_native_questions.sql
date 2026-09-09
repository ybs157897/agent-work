-- 0056_native_questions.sql — Native Kimi AskUserQuestion interactions.
-- Questions remain a separate interaction domain from approvals and steering.
CREATE TABLE questions (
    id            TEXT PRIMARY KEY,
    run_id        TEXT NOT NULL REFERENCES execution_runs(id),
    work_item_id  TEXT NOT NULL REFERENCES work_items(id),
    session_ref   TEXT NOT NULL,
    provider_id   TEXT NOT NULL,
    agent_id      TEXT,
    provider_turn INTEGER NOT NULL DEFAULT 0,
    tool_call_id  TEXT,
    questions_json TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','answered','dismissed','expired')),
    response_json TEXT,
    provider_answer_json TEXT,
    created_at    DATETIME NOT NULL,
    resolved_at   DATETIME,
    UNIQUE(run_id, session_ref, provider_id)
);
CREATE INDEX idx_questions_run_status ON questions(run_id, status, created_at);
