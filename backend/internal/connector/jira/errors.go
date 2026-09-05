package jira

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ctreminiom/go-atlassian/v2/pkg/infra/models"

	"github.com/dbiderman/identityhub/backend/internal/connector"
)

// maxErrorBody caps how much of an upstream error body is inspected. Jira can
// return large HTML pages when a site is misconfigured, and none of it should
// reach our logs.
const maxErrorBody = 8 << 10

// classify turns a go-atlassian failure into a connector error.
//
// This layer stays ours for two reasons. The library collapses several statuses
// into one sentinel -- 403, 429 and 503 all arrive as ErrInvalidStatusCode --
// so the response code is the only reliable signal, and the distinction matters:
// it decides the HTTP status the API returns and whether a Temporal activity
// retries or fails fast.
//
// Upstream text is summarised rather than forwarded. Atlassian error bodies can
// name internal field IDs and workspace configuration, which is not useful to a
// CI pipeline and not ours to echo back.
func classify(res *models.ResponseScheme, err error) error {
	if err == nil {
		return nil
	}
	// No response at all means the request never completed: a timeout, a DNS
	// failure, a refused connection. Transient by assumption.
	if res == nil {
		return connector.Errorf(connector.ErrUnavailable,
			"Could not reach the Jira site. This will be retried automatically.")
	}

	body := parseErrorBody(res)

	switch res.Code {
	case http.StatusUnauthorized:
		return connector.Errorf(connector.ErrUnauthorized,
			"Jira rejected the stored credential. Reconnect the Jira integration to continue.")

	case http.StatusForbidden:
		return connector.Errorf(connector.ErrForbidden,
			"The connected Jira account does not have permission to perform this action.")

	case http.StatusNotFound:
		return connector.Errorf(connector.ErrNotFound,
			"Jira could not find the requested project or issue.")

	case http.StatusTooManyRequests:
		return connector.Errorf(connector.ErrRateLimited,
			"Jira is rate limiting requests. This will be retried automatically.")

	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		e := connector.Errorf(connector.ErrInvalidRequest, "%s", summarise(body,
			"Jira rejected the request. Check that the project and issue type are valid."))
		if len(body.Errors) > 0 {
			e.Fields = body.Errors
		}
		return e
	}

	if res.Code >= 500 {
		return connector.Errorf(connector.ErrUnavailable,
			"Jira is currently unavailable. This will be retried automatically.")
	}
	return connector.Errorf(connector.ErrInvalidRequest,
		"Jira returned an unexpected response.")
}

// jiraErrorBody is Atlassian's standard error envelope.
type jiraErrorBody struct {
	ErrorMessages []string          `json:"errorMessages"`
	Errors        map[string]string `json:"errors"`
}

func parseErrorBody(res *models.ResponseScheme) jiraErrorBody {
	var body jiraErrorBody
	raw := res.Bytes.Bytes()
	if len(raw) > maxErrorBody {
		raw = raw[:maxErrorBody]
	}
	_ = json.Unmarshal(raw, &body)
	return body
}

// summarise prefers Jira's own message when it reads as a sentence rather than
// a diagnostic dump.
func summarise(body jiraErrorBody, fallback string) string {
	if len(body.ErrorMessages) > 0 {
		if msg := strings.TrimSpace(body.ErrorMessages[0]); msg != "" && len(msg) <= 200 {
			return msg
		}
	}
	if len(body.Errors) == 1 {
		for field, reason := range body.Errors {
			if len(reason) <= 160 {
				return field + ": " + reason
			}
		}
	}
	return fallback
}

// jiraTimestamp converts the library's time wrapper, tolerating a missing
// value. An absent creation date is not worth failing a list for.
func jiraTimestamp(d *models.DateTimeScheme) time.Time {
	if d == nil {
		return time.Time{}
	}
	return time.Time(*d)
}
