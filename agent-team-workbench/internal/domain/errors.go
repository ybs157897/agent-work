package domain

import (
	"errors"
	"fmt"
)

// 领域错误；httpapi 层映射为 problem+json（协议文档 §5.5）。
var (
	ErrNotFound            = errors.New("resource not found")
	ErrVersionConflict     = errors.New("resource version conflict")
	ErrIllegalTransition   = errors.New("illegal state transition")
	ErrTerminalImmutable   = errors.New("terminal state is immutable")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different request")
	ErrCapabilityMissing   = errors.New("required capability unavailable")
	ErrValidation          = errors.New("domain validation failed")
	// ErrCursorExpired：SSE 游标已过保留窗口，客户端需重新 bootstrap（HTTP 410）。
	ErrCursorExpired = errors.New("event cursor expired")
)

// TransitionError 携带非法迁移的上下文。
type TransitionError struct {
	Entity string
	From   string
	To     string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("%s: illegal transition %s -> %s", e.Entity, e.From, e.To)
}

func (e *TransitionError) Unwrap() error { return ErrIllegalTransition }
