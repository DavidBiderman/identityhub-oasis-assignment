package connector

import (
	"errors"
	"fmt"
)

// Errors crossing the connector boundary are classified so that callers can act
// on a failure without knowing which provider produced it: the HTTP layer picks
// a status code, and a Temporal activity decides whether to retry.
//
// These classes are provider-independent -- any credentialed HTTP API can
// reject a credential, refuse a permission, or ask you to slow down -- which is
// why they belong here and a provider's own vocabulary does not.
//
// A raw upstream error body is never forwarded verbatim: it is not a useful
// message for a CI pipeline and may disclose details of the customer's tenant.
//
// The sentinels below name a class, in Go's own style: lowercase, unpunctuated,
// because an error is wrapped and a capitalised sentence reads badly in the
// middle of a chain. What a caller is shown is Error.Message, which every
// construction site sets and which is written as a sentence for a person.
var (
	// ErrUnauthorized means the stored credential was rejected. The connection
	// should be marked invalid and the user prompted to reconnect.
	ErrUnauthorized = errors.New("credential rejected")

	// ErrForbidden means the credential is valid but lacks permission for the
	// requested operation.
	ErrForbidden = errors.New("permission denied")

	// ErrNotFound means the referenced resource does not exist, or is not
	// visible to this credential.
	ErrNotFound = errors.New("not found")

	// ErrInvalidRequest means the provider rejected the request as malformed,
	// typically a missing required field or an invalid reference.
	ErrInvalidRequest = errors.New("request rejected by the provider")

	// ErrRateLimited means the provider asked us to slow down. Retryable.
	ErrRateLimited = errors.New("rate limited by the provider")

	// ErrUnavailable means the provider failed or could not be reached.
	// Retryable.
	ErrUnavailable = errors.New("provider unavailable")

	// ErrUnsupportedAction means the connector does not implement the
	// requested action. It is never retryable: no amount of waiting will make
	// a connector grow an operation.
	ErrUnsupportedAction = errors.New("unsupported action")

	// ErrConfigInvalid means a stored configuration could not be decoded into
	// the connector's own type, or failed that type's validation.
	//
	// Decryption succeeding proves only that the bytes are ours. It does not
	// prove they are the right shape: a row could predate a schema change, or
	// belong to a different connector. This error is that distinction.
	ErrConfigInvalid = errors.New("stored configuration is not usable")
)

// Error carries a classified connector failure along with a message that is
// safe to show a user and, where the provider supplied them, the specific
// fields it objected to.
type Error struct {
	Kind    error             // one of the sentinels above
	Message string            // safe for display
	Fields  map[string]string // field name -> reason, when the provider itemised them
}

// Error is the message, and nothing else.
//
// The HTTP layer shows this text to whoever made the request, so it must read
// as an explanation rather than as a diagnostic: no class prefix, no wrapped
// chain. The classification travels through Unwrap, where a caller matching
// with errors.Is can find it, and where a person reading a message cannot.
func (e *Error) Error() string {
	if e.Message == "" {
		return e.Kind.Error()
	}
	return e.Message
}

// Unwrap lets callers match with errors.Is against the sentinels.
func (e *Error) Unwrap() error { return e.Kind }

// Retryable reports whether repeating the request could plausibly succeed. The
// Temporal activities use this to decide between retrying and failing fast, so
// that a permanently rejected credential does not consume a retry budget.
func (e *Error) Retryable() bool {
	return errors.Is(e.Kind, ErrRateLimited) || errors.Is(e.Kind, ErrUnavailable)
}

// Errorf builds a classified error whose message is safe to show a user.
func Errorf(kind error, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// InvalidConfig reports that a configuration field is unusable, naming the
// field and why.
//
// The field travels in the Fields map rather than only inside the message, so
// that a caller can attach it to the input it came from. An earlier version
// formatted it into the message and had the HTTP layer parse it back out with
// string matching, which was fragile in the obvious way and lost the field
// entirely whenever the wording changed.
func InvalidConfig(field, detail string) *Error {
	return &Error{
		Kind:    ErrConfigInvalid,
		Message: detail,
		Fields:  map[string]string{field: detail},
	}
}

// UnsupportedActionError reports that a connector cannot perform an action.
//
// Every connector produces this same error from its own action parsing, so the
// message is identical wherever it comes from and a caller can match it with
// errors.Is against ErrUnsupportedAction.
func UnsupportedActionError(t Type, a Action) *Error {
	return Errorf(ErrUnsupportedAction, "A %s connection cannot perform the action %q.", t, a)
}
