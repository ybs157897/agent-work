package runtime

import "errors"

// ErrQuestionProviderAlreadyResolved means the provider rejected a replay
// because its native interaction is already answered or dismissed. Callers
// must keep the prepared typed answer until the authoritative native event is
// replayed and reconciled.
var ErrQuestionProviderAlreadyResolved = errors.New("question provider interaction already resolved")
