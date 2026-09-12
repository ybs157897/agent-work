package knowledgelib

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors shared by the library package. They are translated to
// domain errors at the application boundary.
var (
	// ErrValidation reports input that the library refuses to accept.
	ErrValidation = errors.New("knowledge library validation failed")
	// ErrStateConflict reports a concurrent or out-of-order state change.
	ErrStateConflict = errors.New("knowledge library state conflict")
	// ErrNotFound reports a missing library object.
	ErrNotFound = errors.New("knowledge library object not found")
	// ErrBlocked reports a task that cannot proceed without operator input.
	ErrBlocked = errors.New("knowledge library task blocked")
)

func errf(base error, format string, args ...any) error {
	return fmt.Errorf("%w: %s", base, fmt.Sprintf(format, args...))
}

// digestParts folds several values into one stable digest.
func digestParts(prefix string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return prefix + hex.EncodeToString(h.Sum(nil))
}

// ValidationErrors aggregates every problem found in one validation pass.
// Reporting them together is what lets a single bounded repair turn fix all
// of them; reporting only the first would spend the repair budget on
// discovering problems one at a time.
type ValidationErrors struct {
	Problems []string
}

// Add records one problem, ignoring nil.
func (e *ValidationErrors) Add(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	for _, existing := range e.Problems {
		if existing == msg {
			return
		}
	}
	e.Problems = append(e.Problems, msg)
}

// Addf records one formatted problem.
func (e *ValidationErrors) Addf(format string, args ...any) {
	e.Add(fmt.Errorf(format, args...))
}

// Len reports how many distinct problems were recorded.
func (e *ValidationErrors) Len() int { return len(e.Problems) }

// OrNil returns nil when nothing was recorded.
func (e *ValidationErrors) OrNil() error {
	if len(e.Problems) == 0 {
		return nil
	}
	return e
}

func (e *ValidationErrors) Error() string {
	if len(e.Problems) == 1 {
		return e.Problems[0]
	}
	return fmt.Sprintf("共 %d 处问题：\n- %s", len(e.Problems), strings.Join(e.Problems, "\n- "))
}

// Unwrap keeps errors.Is(err, ErrValidation) true without prefixing the
// message twice at every wrapping layer.
func (e *ValidationErrors) Unwrap() error { return ErrValidation }
