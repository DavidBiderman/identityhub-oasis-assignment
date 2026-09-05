// The product's own rules about what a request may contain.
//
// They run before a request reaches a provider, so that a caller gets a precise
// reason instead of an opaque upstream refusal, and a failure names every bad
// field at once: fixing a form one round trip per field is a bad product.
//
// What is *not* here is any check on fields we do not recognise. An unknown
// field is ignored rather than reported, because reporting it would answer a
// question a caller has no business asking -- whether a field exists. Nothing
// is at risk either way: a request type has no organization or account field,
// so a body naming one is inert, and saying "unrecognised field accountId"
// would only confirm that the name is worth trying.
package httpapi

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Field limits. These are the product's own constraints.
const (
	maxTitleLen          = 255
	maxDescriptionLen    = 32000
	maxNameLen           = 100
	maxLabels            = 10
	maxIdempotencyKeyLen = 200
)

// projectKeyPattern matches the shape trackers use for a project key.
var projectKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// labelPattern rejects whitespace, which trackers generally disallow in labels.
var labelPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// invalid collects the fields a caller must fix.
type invalid map[string]string

// add records one rejection. The first reason for a field is kept, since it is
// the most specific: "is required" beats "is too long" for an empty value.
func (in invalid) add(field, format string, args ...any) {
	if _, seen := in[field]; !seen {
		in[field] = fmt.Sprintf(format, args...)
	}
}

// err is the failure, or nil when nothing was rejected.
func (in invalid) err() error {
	if len(in) == 0 {
		return nil
	}
	f := fault(http.StatusUnprocessableEntity, codeValidationFailed,
		"One or more fields are invalid.")
	f.Fields = in
	return f
}

// text checks a required free-text field.
func (in invalid) text(field, value string, max int) {
	switch {
	case value == "":
		in.add(field, "This field is required.")
	case utf8.RuneCountInString(value) > max:
		in.add(field, "Must be at most %d characters.", max)
	}
}

// projectKey checks a tracker project key.
func (in invalid) projectKey(value string) {
	switch {
	case value == "":
		in.add("projectKey", "This field is required.")
	case !projectKeyPattern.MatchString(value):
		in.add("projectKey", "Must be uppercase letters, digits or underscores, "+
			"starting with a letter, for example NHI.")
	}
}

// labels checks an optional label list.
//
// A rejected label is reported by position rather than by value: the value came
// from the caller, and echoing it back into a message is how a response becomes
// a reflection point.
func (in invalid) labels(values []string) {
	if len(values) > maxLabels {
		in.add("labels", "At most %d labels are allowed.", maxLabels)
		return
	}
	for i, label := range values {
		if !labelPattern.MatchString(label) {
			in.add("labels", "Label %d must be 1 to 64 letters, digits, dots, "+
				"hyphens or underscores.", i+1)
			return
		}
	}
}

// finding checks the fields both ticket-creating endpoints share.
func (in invalid) finding(projectKey, title, description string, labels []string) {
	in.projectKey(projectKey)
	in.text("title", title, maxTitleLen)
	in.text("description", description, maxDescriptionLen)
	in.labels(labels)
}

// Validate normalises and checks a connect request.
//
// The configuration itself is not checked here. Only the connector knows what
// a valid configuration for it looks like, and it validates its own.
func (r *ConnectRequest) Validate() error {
	r.ConnectorType = strings.TrimSpace(r.ConnectorType)

	in := invalid{}
	if r.ConnectorType == "" {
		in.add("connectorType", "This field is required.")
	}
	if len(r.Config) == 0 {
		in.add("config", "This field is required.")
	}
	return in.err()
}

// Validate normalises and checks a ticket request from the interface.
func (r *CreateTicketRequest) Validate() error {
	r.ProjectKey = strings.TrimSpace(r.ProjectKey)
	r.IssueTypeID = strings.TrimSpace(r.IssueTypeID)
	r.Title = strings.TrimSpace(r.Title)
	r.Description = strings.TrimSpace(r.Description)

	in := invalid{}
	in.finding(r.ProjectKey, r.Title, r.Description, nil)
	return in.err()
}

// Validate normalises and checks a finding from the public API.
func (r *CreateFindingRequest) Validate() error {
	r.ProjectKey = strings.TrimSpace(r.ProjectKey)
	r.IssueTypeID = strings.TrimSpace(r.IssueTypeID)
	r.Title = strings.TrimSpace(r.Title)
	r.Description = strings.TrimSpace(r.Description)

	in := invalid{}
	in.finding(r.ProjectKey, r.Title, r.Description, r.Labels)
	return in.err()
}

// Validate normalises and checks an API key request.
func (r *CreateAPIKeyRequest) Validate() error {
	r.Name = strings.TrimSpace(r.Name)

	in := invalid{}
	in.text("name", r.Name, maxNameLen)
	return in.err()
}

// idempotencyKey checks the header the public API honours.
func idempotencyKey(presented string) (string, error) {
	key := strings.TrimSpace(presented)
	if utf8.RuneCountInString(key) > maxIdempotencyKeyLen {
		in := invalid{}
		in.add("Idempotency-Key", "Must be at most %d characters.", maxIdempotencyKeyLen)
		return "", in.err()
	}
	return key, nil
}

// value reads an optional parameter, which the generated types carry as a
// pointer because the spec says it may be absent.
func value[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// projectKeyParam reads a project key from the path or the query string.
func projectKeyParam(value string) (string, error) {
	in := invalid{}
	in.projectKey(value)
	return value, in.err()
}

// boundedParam checks a page size, or returns the default when none was sent.
//
// The upper bound is the point: a caller-supplied page size with no ceiling is
// a table scan someone can ask for. It is refused rather than silently reduced,
// so a caller who asks for a thousand rows learns they will not get them.
//
// The spec declares the same minimum, maximum and default. That is
// documentation -- it tells a client what to send and it is what Swagger shows
// -- and this function is the enforcement. Nothing validates a request body or
// query against the spec at runtime; the two copies are kept in step by hand,
// and the e2e suite checks responses against the spec, not requests.
func boundedParam(name string, sent *int, fallback, max int) (int, error) {
	if sent == nil {
		return fallback, nil
	}

	in := invalid{}
	switch {
	case *sent <= 0:
		in.add(name, "Must be a positive whole number.")
	case *sent > max:
		in.add(name, "Must be at most %d.", max)
	}
	if err := in.err(); err != nil {
		return 0, err
	}
	return *sent, nil
}
