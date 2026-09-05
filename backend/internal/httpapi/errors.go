// How a failure becomes a response.
//
// The rule here is that an error carries its own message and this layer does
// not rewrite it. Whatever failed is the only thing that knew what went wrong,
// so it says so, in words meant for whoever made the request; this file picks
// the status code and the machine-readable code, and copies the message across.
//
// There is one exception, at the bottom: an error nobody classified was never
// written for a caller and may describe internal structure, so it is logged and
// answered with a fixed message instead.
package httpapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"go.temporal.io/sdk/temporal"

	"github.com/dbiderman/identityhub/backend/internal/connections"
	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/store"
)

// Failure codes. They are part of the API: a client may branch on them, so
// they change only when the meaning does.
const (
	codeMalformed          = "malformed_request"
	codeValidationFailed   = "validation_failed"
	codeUnauthorized       = "unauthorized"
	codeTokenNotNameable   = "token_not_nameable"
	codeForbidden          = "forbidden"
	codeNotFound           = "not_found"
	codeConflict           = "conflict"
	codeInternal           = "internal_error"
	codeNotConnected       = "not_connected"
	codeCredentialRejected = "credential_rejected"
	codeConnectionUnusable = "connection_unusable"
	codeProviderRejected   = "provider_rejected"
	codeProviderNotFound   = "provider_not_found"
	codeProviderRateLimit  = "provider_rate_limited"
	codeProviderDown       = "provider_unavailable"
	codeUnsupported        = "unsupported_operation"
	codeIdempotencyReused  = "idempotency_key_reused"
	codeIdempotencyRunning = "idempotency_key_in_progress"
	codeQueueUnavailable   = "queue_unavailable"
	codeDependencyDown     = "dependency_unavailable"
)

// Fault is an error that already knows how it should be reported.
//
// A handler returns one when this layer is what knew: a body that is not JSON,
// fields a caller must fix, an identifier that is not a UUID. Errors from below
// arrive classified instead, and translate maps those.
type Fault struct {
	Status int
	Failure
}

func (f *Fault) Error() string { return f.Message }

// fault builds one.
func fault(status int, code, format string, args ...any) *Fault {
	return &Fault{
		Status:  status,
		Failure: Failure{Code: code, Message: fmt.Sprintf(format, args...)},
	}
}

// classes say how each classified failure is reported.
//
// This table is the whole reason the connector error taxonomy exists: it lets
// the API answer correctly without knowing which provider failed. An upstream
// outage becomes 502 rather than a 500 that tells a caller nothing, and a
// rejected credential becomes a 409 whose remedy is to reconnect.
var classes = []struct {
	kind   error
	status int
	code   string
	// fallback is shown only when the error carried no message of its own.
	// Every connector failure carries one, so these are for a sentinel returned
	// bare -- whose own text names a class in Go's style and is not something
	// to put in front of a person.
	fallback string
}{
	{connector.ErrUnauthorized, http.StatusConflict, codeCredentialRejected,
		"The stored credential was rejected. Reconnect the integration to continue."},
	{connector.ErrForbidden, http.StatusForbidden, codeForbidden,
		"The connected account does not have permission to perform this action."},
	{connector.ErrNotFound, http.StatusNotFound, codeProviderNotFound,
		"The requested item does not exist, or is not visible to the connected account."},
	{connector.ErrInvalidRequest, http.StatusUnprocessableEntity, codeProviderRejected,
		"The provider rejected the request."},
	{connector.ErrRateLimited, http.StatusServiceUnavailable, codeProviderRateLimit,
		"The provider is rate limiting requests. Try again shortly."},
	// 502, not 500: the failure is upstream, and saying so tells a caller that
	// retrying is reasonable.
	{connector.ErrUnavailable, http.StatusBadGateway, codeProviderDown,
		"The provider could not be reached. Try again shortly."},
	{connector.ErrConfigInvalid, http.StatusConflict, codeConnectionUnusable,
		"The stored connection could not be used. Reconnect the integration to continue."},
	{connector.ErrUnsupportedAction, http.StatusBadRequest, codeUnsupported,
		"This integration does not support that operation."},
	{connections.ErrNotConnected, http.StatusConflict, codeNotConnected,
		connections.ErrNotConnected.Error()},
	{store.ErrNotFound, http.StatusNotFound, codeNotFound,
		"The requested resource does not exist."},
	{store.ErrConflict, http.StatusConflict, codeConflict,
		"That resource already exists."},
}

// translate decides how an error is reported.
func translate(err error) *Fault {
	var f *Fault
	if errors.As(err, &f) {
		return f
	}

	for _, class := range classes {
		if !errors.Is(err, class.kind) {
			continue
		}
		// A connector that failed said something specific, and itemised the
		// fields it objected to. Prefer what it said; the fallback is for a
		// sentinel returned bare.
		reported := &Fault{Status: class.status,
			Failure: Failure{Code: class.code, Message: class.fallback}}

		var ce *connector.Error
		if errors.As(err, &ce) {
			reported.Message, reported.Fields = ce.Error(), ce.Fields
		}
		return reported
	}

	// Echo raises these for a route that does not exist or a method that is
	// not allowed. They are the framework's, not ours.
	var he *echo.HTTPError
	if errors.As(err, &he) && he.Code < http.StatusInternalServerError {
		switch he.Code {
		case http.StatusNotFound:
			return fault(he.Code, codeNotFound, "No such endpoint.")
		case http.StatusMethodNotAllowed:
			return fault(he.Code, codeMalformed, "That method is not supported on this endpoint.")
		case http.StatusRequestEntityTooLarge:
			return fault(he.Code, codeMalformed, "The request body is larger than %s.", maxBodySize)
		default:
			return fault(he.Code, codeMalformed, "%s", http.StatusText(he.Code))
		}
	}

	return internalFault()
}

// internalFault answers an error nobody classified.
func internalFault() *Fault {
	return fault(http.StatusInternalServerError, codeInternal,
		"The request could not be completed. If it keeps happening, quote the "+
			"request ID from the response headers.")
}

// translateWorkflowError maps a Temporal failure onto a response.
//
// Temporal serialises an activity's error, so errors.Is against the connector
// sentinels no longer matches on the way back. What survives is what the
// activity attached when it marked the failure non-retryable: a type, which
// selects the status here, and the message it was given -- which is the
// connector's own message, carried through unchanged.
func translateWorkflowError(err error) *Fault {
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) {
		return translate(err)
	}

	message := appErr.Message()
	switch appErr.Type() {
	case "unauthorized":
		return fault(http.StatusConflict, codeCredentialRejected, "%s", message)
	case "forbidden":
		return fault(http.StatusForbidden, codeForbidden, "%s", message)
	case "not_found":
		return fault(http.StatusNotFound, codeProviderNotFound, "%s", message)
	case "invalid_request":
		return fault(http.StatusUnprocessableEntity, codeProviderRejected, "%s", message)
	case "not_connected":
		return fault(http.StatusConflict, codeNotConnected, "%s", message)
	case "configuration":
		return fault(http.StatusConflict, codeConnectionUnusable, "%s", message)
	case "unsupported":
		return fault(http.StatusBadRequest, codeUnsupported, "%s", message)
	case "idempotency_mismatch":
		return fault(http.StatusConflict, codeIdempotencyReused, "%s", message)
	case "idempotency_in_progress":
		return fault(http.StatusConflict, codeIdempotencyRunning, "%s", message)
	default:
		// Reached only by a failure nobody classified. It says nothing about
		// retrying, because nothing here knows whether the workflow will: an
		// activity that marked a failure non-retryable named its type, and
		// every named type has a case above.
		return fault(http.StatusBadGateway, codeProviderDown,
			"The ticket could not be filed. Check the recent tickets list before filing it again.")
	}
}

// ok sends a successful response.
func ok(c echo.Context, status int, data any) error {
	return c.JSON(status, Response{Data: data})
}

// render is Echo's error handler: every error a handler returns arrives here,
// and this is the only place a failure is written.
//
// Handlers therefore return errors rather than responses. An earlier version
// had helpers that wrote a failure and returned nil, which meant a handler
// could not tell "handled" from "fine" and occasionally wrote two responses to
// one request.
func (s *Server) render(err error, c echo.Context) {
	if err == nil || c.Response().Committed {
		return
	}

	f := translate(err)
	if f.Status >= http.StatusInternalServerError {
		s.log.Error("request failed",
			"path", c.Path(), "request_id", c.Get("request_id"), "error", err)
	}
	_ = c.JSON(f.Status, Response{Error: &f.Failure})
}
