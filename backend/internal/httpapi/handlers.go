// Handlers for every endpoint.
//
// One per operation in api/openapi.yaml, all in this file: small functions that
// share a Server, a set of guards and one error vocabulary are easier to read
// together than spread across six files. The count is deliberately not written
// down here -- it would be wrong within a week.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/dbiderman/identityhub/backend/internal/auth"
	"github.com/dbiderman/identityhub/backend/internal/connections"
	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/httpapi/apigen"
	"github.com/dbiderman/identityhub/backend/internal/store"
	"github.com/dbiderman/identityhub/backend/internal/store/sqlcgen"
	"github.com/dbiderman/identityhub/backend/internal/workflows"
)

// There is no login or logout endpoint.
//
// This application does not authenticate anyone: an identity provider issues an
// access token, and the interface presents it. Signing in and out are the
// provider's, and adding them here would mean holding a credential this
// application has no business holding.

// me echoes the caller's own claims.
//
// It reports the account being acted in, which is worth showing — but note that
// returning it changes nothing about trust. The value came from the token, and
// sending it back does not make it settable.
func (s *Server) WhoAmI(c echo.Context) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}

	roles := make([]string, 0, len(principal.Roles))
	for _, r := range principal.Roles {
		roles = append(roles, string(r))
	}

	return ok(c, http.StatusOK, MeResponse{
		Subject:   principal.Subject,
		Email:     principal.Email,
		Name:      principal.Name,
		Roles:     roles,
		OrgID:     principal.Scope.OrgID.String(),
		AccountID: principal.Scope.AccountID.String(),
	})
}

// SignOut stops this application from accepting the caller's token.
//
// The brief asks for logout, and a logout that only clears the browser is not
// one: a token copied out beforehand would keep working until it expired. This
// is the server half -- from here the token is refused however it is presented
// and from wherever.
//
// It does not end the identity provider's session. That is the provider's to
// end, and in a real deployment the interface follows this call with an
// RP-initiated logout redirect.
func (s *Server) SignOut(c echo.Context) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}

	// A token with no jti cannot be named, so it cannot be refused later. That
	// is a property of the credential rather than of this request, so it is 422
	// -- the request was understood and cannot be carried out -- and it names
	// the claim a provider has to emit.
	if principal.TokenID == "" {
		return fault(http.StatusUnprocessableEntity, codeTokenNotNameable,
			"This token carries no jti claim, so it cannot be signed out. "+
				"Configure the identity provider to issue one.")
	}

	ctx := c.Request().Context()
	if err := s.revocations.Revoke(
		ctx, principal.TokenID, principal.Subject, principal.ExpiresAt); err != nil {
		return fmt.Errorf("sign out the token for %q: %w", principal.Subject, err)
	}

	s.audit(c, principal, store.ActionUserSignedOut, principal.Subject, map[string]any{
		// The jti names which token, not what it grants -- it is an identifier,
		// not a credential. Recording it is how "this one was signed out at
		// 14:02" becomes answerable.
		"tokenId":   principal.TokenID,
		"expiresAt": principal.ExpiresAt.UTC().Format(time.RFC3339),
	})

	return c.NoContent(http.StatusNoContent)
}

// SignIn exchanges an email address and a password for an access token.
//
// This endpoint is what a real deployment does not have. An identity provider
// authenticates the person and issues the token; this application reads it
// either way. What makes that swap a one-file change is that the token minted
// here is the same token every other path already consumes -- same claims, same
// tenancy, same signature check, same revocation.
//
// Unauthenticated by necessity: it is how a caller obtains a credential.
func (s *Server) SignIn(c echo.Context) error {
	var body SignInRequest
	if err := c.Bind(&body); err != nil {
		return fault(http.StatusBadRequest, codeMalformed,
			"The request body is not valid JSON.")
	}

	result, err := s.signIn.Authenticate(c.Request().Context(), body.Email, body.Password)
	if err != nil {
		if errors.Is(err, auth.ErrBadCredentials) {
			// Logged, not audited. audit_events is tenant-scoped -- org_id and
			// account_id are not null -- and a failed sign-in has no tenancy,
			// because not knowing who the caller is is the whole point of it
			// failing. A burst of these from one address is what a brute-force
			// attempt looks like, and this is where it is visible. The address
			// typed is recorded; the password is not, ever.
			s.log.Warn("sign-in failed",
				"email", body.Email, "ip", clientIP(c), "request_id", c.Get("request_id"))

			// One answer for an unknown address and a wrong password. Saying
			// which would turn this into a way to ask who has an account here.
			return fault(http.StatusUnauthorized, codeUnauthorized, "%s", err.Error())
		}
		return fmt.Errorf("sign in %q: %w", body.Email, err)
	}

	// The jti and the address it was issued to.
	//
	// A bearer token cannot tell us later who is holding it, so the trail has to
	// be built at the moment it is handed out. With this, "this token was issued
	// to one address at 10:00 and used from another at 10:05" is a question the
	// audit trail can answer -- which is the same question this product exists
	// to ask about non-human identities.
	s.audit(c, auth.Principal{
		Scope:     result.Scope,
		ActorType: store.ActorUser,
		ActorID:   result.UserID,
		Subject:   result.Subject,
	}, store.ActionUserSignedIn, result.Subject, map[string]any{
		"tokenId": result.TokenID,
	})

	return ok(c, http.StatusOK, SignInResponse{
		AccessToken: result.Token,
		TokenType:   "Bearer",
		ExpiresIn:   int(s.signIn.TokenTTL().Seconds()),
	})
}

// audit records a security-relevant action, logging rather than failing when it
// cannot: losing an audit line is bad, and failing the action over it is worse.
//
// Nothing updates or deletes an audit event, and the application database role
// has UPDATE and DELETE revoked on the table, so the trail is append-only in the
// database as well as here. Metadata must never carry credential material.
func (s *Server) audit(c echo.Context, p auth.Principal, action, target string, metadata map[string]any) {
	ctx := c.Request().Context()

	encoded, err := json.Marshal(metadata)
	if err == nil {
		err = store.InTx(ctx, s.pool, func(q *sqlcgen.Queries) error {
			return q.AppendAuditEvent(ctx, sqlcgen.AppendAuditEventParams{
				OrgID:     p.Scope.OrgID,
				AccountID: p.Scope.AccountID,
				ActorType: string(p.ActorType),
				// NULL, not the zero UUID. UpsertUser is deliberately
				// non-fatal, so a principal can reach here with no actor row --
				// and a zero UUID in this column looks like a real actor that
				// resolves to nobody. The workflow side of the same column
				// already writes NULL.
				ActorID:  store.NullableUUID(p.ActorID),
				Action:   action,
				Target:   target,
				Metadata: encoded,
				Ip:       clientIP(c),
			})
		})
	}
	if err != nil {
		s.log.Warn("could not write audit event",
			"action", action, "request_id", c.Get("request_id"), "error", err)
	}
}

// liveStatusBudget is how long the recent-tickets list will wait for the
// provider before rendering without live status.
const liveStatusBudget = 3 * time.Second

// The endpoint below stands in for an identity provider's authorization
// endpoint. It is mounted only when the development resolver is in use, and
// that resolver is refused when APP_ENV=production.
//
// It exists so that a reviewer can act as different people in different
// accounts and see that the isolation holds. In a real deployment the provider
// does this, the interface redirects to it, and neither this handler nor the
// tokens it mints exist.

// listConnections returns the account's integrations.
//
// The encrypted configuration is never read for a listing, so no credential is
// decrypted to render this page.
func (s *Server) ListConnections(c echo.Context) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}

	conns, err := store.Read(s.pool).ListConnections(c.Request().Context(), sqlcgen.ListConnectionsParams{
		OrgID: principal.Scope.OrgID, AccountID: principal.Scope.AccountID,
	})
	if err != nil {
		return fmt.Errorf("list connections: %w", err)
	}

	return ok(c, http.StatusOK, ConnectionListResponse{
		Connections:    conns,
		AvailableTypes: s.availableTypes(),
	})
}

// connect stores an integration credential after verifying it works.
func (s *Server) Connect(c echo.Context) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}

	var req ConnectRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return fault(http.StatusBadRequest, codeMalformed, "The request body is not valid JSON.")
	}

	if err := req.Validate(); err != nil {
		return err
	}

	connectorType := connector.Type(req.ConnectorType)
	ctx := c.Request().Context()
	owner, connectionID, err := s.connections.Save(ctx, principal.Scope, connectorType,
		req.Config, principal.ActorID)
	if err != nil {
		// A configuration the connector rejected is this caller's input, not a
		// stored connection gone bad, so it is reported as a rejected field
		// rather than as the conflict that same class means everywhere else.
		// The connector named the field; nothing here has to guess it.
		var invalidConfig *connector.Error
		if errors.As(err, &invalidConfig) && errors.Is(invalidConfig, connector.ErrConfigInvalid) {
			rejected := fault(http.StatusUnprocessableEntity, codeValidationFailed,
				"One or more fields are invalid.")
			rejected.Fields = invalidConfig.Fields
			if len(rejected.Fields) == 0 {
				rejected.Fields = map[string]string{"config": invalidConfig.Error()}
			}
			return rejected
		}
		return err
	}

	// The account whose credential this is, so the audit trail records which
	// identity the integration acts as. Never the credential.
	s.audit(c, principal, store.ActionJiraConnected, string(connectorType), map[string]any{
		"ownerEmail": owner.Email,
		"ownerName":  owner.DisplayName,
	})

	// Connecting starts the recurring digest for this account.
	s.ensureDigestSchedule(ctx, principal.Scope, connectorType)

	return ok(c, http.StatusCreated, ConnectedResponse{
		ID:            connectionID,
		ConnectorType: connectorType,
		Owner:         owner,
	})
}

// disconnect revokes an integration.
func (s *Server) Disconnect(c echo.Context, connectorTypeParam string) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}

	connectorType := connector.Type(connectorTypeParam)
	ctx := c.Request().Context()

	// The row count is the difference between a 404 and a 204: nothing revoked
	// means there was nothing to revoke.
	var revoked int64
	err = store.InTx(ctx, s.pool, func(q *sqlcgen.Queries) error {
		var err error
		revoked, err = q.RevokeConnectionsOfType(ctx, sqlcgen.RevokeConnectionsOfTypeParams{
			OrgID:         principal.Scope.OrgID,
			AccountID:     principal.Scope.AccountID,
			ConnectorType: string(connectorType),
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("revoke the %s connection: %w", connectorType, err)
	}
	if revoked == 0 {
		return fault(http.StatusNotFound, codeNotFound,
			"This account has no active %s connection.", connectorType)
	}

	s.audit(c, principal, store.ActionJiraDisconnected, string(connectorType), nil)

	// Disconnecting stops the digest: it has no credential to run with.
	s.removeDigestSchedule(ctx, principal.Scope, connectorType)

	return c.NoContent(http.StatusNoContent)
}

// listProjects returns the targets a finding can be filed into.
func (s *Server) ListTargets(c echo.Context, params apigen.ListTargetsParams) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}

	conn, err := s.activeConnection(c, principal.Scope)
	if err != nil {
		return err
	}

	targets, err := conn.ListTargets(c.Request().Context(), value(params.Query), 50)
	if err != nil {
		s.connections.NoteFailure(c.Request().Context(), principal.Scope, conn.ID, err)
		return err
	}
	return ok(c, http.StatusOK, ProjectListResponse{Projects: targets})
}

// listIssueTypes returns the kinds of ticket a target accepts.
func (s *Server) ListKinds(c echo.Context, key apigen.ProjectKey) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}

	projectKey, err := projectKeyParam(key)
	if err != nil {
		return err
	}

	conn, err := s.activeConnection(c, principal.Scope)
	if err != nil {
		return err
	}

	kinds, err := conn.ListKinds(c.Request().Context(), projectKey)
	if err != nil {
		s.connections.NoteFailure(c.Request().Context(), principal.Scope, conn.ID, err)
		return err
	}
	return ok(c, http.StatusOK, IssueTypeListResponse{IssueTypes: kinds})
}

// activeConnection resolves the account's stored connection, ready to act on.
func (s *Server) activeConnection(c echo.Context, scope store.Scope) (connections.Connection, error) {
	return s.connections.Configure(c.Request().Context(), scope, s.defaultConnectorType())
}

// availableTypes reports what could be connected, and what each one needs.
func (s *Server) availableTypes() []ConnectorTypeResponse {
	types := s.registry.Types()

	out := make([]ConnectorTypeResponse, 0, len(types))
	for _, t := range types {
		conn, err := s.registry.Get(t)
		if err != nil {
			// Unreachable: the type came from the registry a line ago.
			continue
		}
		requirements := conn.Requirements()
		out = append(out, ConnectorTypeResponse{
			Type: t, Fields: requirements.Fields, Setup: requirements.Setup,
		})
	}
	return out
}

// defaultConnectorType is the integration the UI drives.
//
// One connector is registered today, so the UI does not ask which to use. When
// a second arrives this becomes a parameter on the request; the change is
// confined to this layer, since everything below already takes a type.
func (s *Server) defaultConnectorType() connector.Type {
	types := s.registry.Types()
	if len(types) == 0 {
		return ""
	}
	return types[0]
}

// What each workflow is called in Temporal, before its unique suffix.
//
// A reader scanning the executions list should be able to tell what happened
// without opening anything.
const (
	fileFindingWorkflow = "file-finding"
	blogDigestWorkflow  = "blog-digest"
)

// workflowID names a workflow for the action it performs, then makes it unique.
func workflowID(action string) string {
	return action + "-" + uuid.NewString()
}

// syncWait is how long a request waits for a durable workflow before handing
// back a status URL instead.
//
// Making ticket creation durable must not make it feel asynchronous. The brief
// requires a clickable link to the created issue, and "queued" is a worse
// product than "created NHI-123". So the caller waits briefly for the common
// case and only falls back to 202 when the provider is genuinely slow -- the
// workflow keeps retrying either way.
const syncWait = 8 * time.Second

// The recent tickets list: the count the brief asks for, and a ceiling on what
// a caller may ask for instead.
const (
	defaultRecentTickets = 10
	maxRecentTickets     = 50
)

// The audit list, likewise.
const (
	defaultAuditEvents = 50
	maxAuditEvents     = 200
)

// listTickets returns the most recent tickets this application created.
//
// The local table is the source of truth for "created from this app": a
// provider-side label is mutable by anyone with access and cannot answer that
// question. Live status is layered on top where it is available.
func (s *Server) ListTickets(c echo.Context, params apigen.ListTicketsParams) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()

	limit, err := boundedParam("limit", params.Limit, defaultRecentTickets, maxRecentTickets)
	if err != nil {
		return err
	}

	// Filtering by project is optional; an empty filter is not a bad one.
	projectKey := value(params.ProjectKey)
	if projectKey != "" {
		if _, err := projectKeyParam(projectKey); err != nil {
			return err
		}
	}

	tickets, err := store.Read(s.pool).RecentTickets(ctx, sqlcgen.RecentTicketsParams{
		OrgID:      principal.Scope.OrgID,
		AccountID:  principal.Scope.AccountID,
		ProjectKey: projectKey,
		RowLimit:   int32(limit),
	})
	if err != nil {
		return fmt.Errorf("list recent tickets: %w", err)
	}

	out := make([]TicketResponse, 0, len(tickets))
	for _, t := range tickets {
		out = append(out, TicketResponse{
			ID: t.ID.String(), ProjectKey: t.ProjectKey, IssueKey: t.IssueKey,
			IssueURL: t.IssueUrl, Title: t.Summary, Source: t.Source, CreatedAt: t.CreatedAt,
		})
	}

	// Live status is a nicety, not a requirement: if the provider is
	// unreachable the list still renders from our own records.
	s.hydrateStatus(ctx, principal.Scope, out)

	return ok(c, http.StatusOK, TicketListResponse{Tickets: out})
}

// hydrateStatus fills in current provider status in one bulk call.
//
// Bounded separately from the request, and deliberately tightly. This is
// decoration: the list renders from our own records whether or not the provider
// answers, so a slow-but-not-dead Jira must not hold the page. Without this the
// only ceiling was the shared outbound client's, which is twenty seconds --
// long enough to make a page load look broken, and to occupy a pool connection
// while it does.
func (s *Server) hydrateStatus(ctx context.Context, scope store.Scope, tickets []TicketResponse) {
	if len(tickets) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, liveStatusBudget)
	defer cancel()

	conn, err := s.connections.Configure(ctx, scope, s.defaultConnectorType())
	if err != nil {
		return
	}

	keys := make([]string, 0, len(tickets))
	for _, t := range tickets {
		keys = append(keys, t.IssueKey)
	}

	// One request for every row, rather than one per row.
	live, err := conn.FetchTickets(ctx, keys)
	if err != nil {
		return
	}

	status := make(map[string]string, len(live))
	for _, ref := range live {
		status[ref.Key] = ref.Status
	}
	for i := range tickets {
		tickets[i].Status = status[tickets[i].IssueKey]
	}
}

// getTicket returns one recorded ticket.
//
// It exists because respondFiled sets a Location header pointing here, and a
// caller that follows it -- which RFC 9110 says a 201 invites -- was getting a
// 404 from a route nobody had written.
func (s *Server) GetTicket(c echo.Context, id uuid.UUID) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}

	ticket, err := store.Read(s.pool).TicketByID(c.Request().Context(), sqlcgen.TicketByIDParams{
		OrgID: principal.Scope.OrgID, AccountID: principal.Scope.AccountID, ID: id,
	})
	if err != nil {
		if errors.Is(store.MapError(err), store.ErrNotFound) {
			// A ticket in another account is reported as not found, so the
			// response cannot be used to discover that it exists.
			return fault(http.StatusNotFound, codeNotFound,
				"This account has no ticket with that identifier.")
		}
		return fmt.Errorf("read ticket %s: %w", id, err)
	}

	return ok(c, http.StatusOK, TicketResponse{
		ID: ticket.ID.String(), ProjectKey: ticket.ProjectKey, IssueKey: ticket.IssueKey,
		IssueURL: ticket.IssueUrl, Title: ticket.Summary, Source: ticket.Source,
		CreatedAt: ticket.CreatedAt,
	})
}

// createTicket files a finding from the UI.
func (s *Server) CreateTicket(c echo.Context) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}

	var req CreateTicketRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return fault(http.StatusBadRequest, codeMalformed, "The request body is not valid JSON.")
	}

	if err := req.Validate(); err != nil {
		return err
	}

	return s.fileFinding(c, principal, workflows.NewFinding{
		TargetKey:   req.ProjectKey,
		KindID:      req.IssueTypeID,
		Title:       req.Title,
		Description: req.Description,
		Labels:      []string{"identityhub"},
	}, store.SourceUI, "")
}

// fileFinding starts the workflow and waits briefly for its result.
//
// The internal command assembled here is where the external request and the
// authenticated principal come together: the finding comes from the body, and
// the organization and account come from the credential. There is no path by
// which the body could supply them.
func (s *Server) fileFinding(
	c echo.Context,
	principal auth.Principal,
	finding workflows.NewFinding,
	source string,
	idempotencyKey string,
) error {
	ctx := c.Request().Context()

	input := workflows.FileTicketInput{
		OrgID:          principal.Scope.OrgID,
		AccountID:      principal.Scope.AccountID,
		ConnectorType:  s.defaultConnectorType(),
		Finding:        finding,
		Source:         source,
		ActorType:      principal.ActorType,
		ActorID:        principal.ActorID,
		IdempotencyKey: idempotencyKey,
	}

	run, err := s.temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		TaskQueue: s.taskQueue,
		// Named for what it does, then made unique.
		//
		// Temporal generates a bare UUID when no ID is given, and a list of
		// those tells whoever is looking at it nothing: every execution in the
		// interface looks the same, and finding the one that matters means
		// opening them one at a time. The prefix is what makes the list
		// readable, and it costs a string.
		//
		// The suffix is random rather than derived, because a deterministic ID
		// would collide across legitimate repeat filings -- the same finding
		// filed twice on purpose is two workflows. Idempotency is handled
		// explicitly by the Idempotency-Key mechanism instead, which is a
		// different question with a different answer.
		ID:                       workflowID(fileFindingWorkflow),
		WorkflowExecutionTimeout: 10 * time.Minute,
	}, workflows.FileTicket, input)
	if err != nil {
		// The queue being down is not this caller's fault and not something a
		// message about it can hide, so it is reported as retryable and the
		// cause is logged rather than shown.
		s.log.Error("could not start the ticket workflow",
			"request_id", c.Get("request_id"), "error", err)
		return fault(http.StatusServiceUnavailable, codeQueueUnavailable,
			"The processing queue is unavailable. Try again shortly.")
	}

	waitCtx, cancel := context.WithTimeout(ctx, syncWait)
	defer cancel()

	var result workflows.FileTicketResult
	err = run.Get(waitCtx, &result)

	switch {
	case err == nil:
		return s.respondFiled(c, result)

	case errors.Is(waitCtx.Err(), context.DeadlineExceeded):
		// The workflow is still running and will keep retrying. Report that
		// honestly rather than failing a request that may yet succeed.
		return ok(c, http.StatusAccepted, PendingTicketResponse{
			Status:     "pending",
			WorkflowID: run.GetID(),
			Detail: "The ticket is still being created. " +
				"It will appear in the recent tickets list once it is filed.",
		})

	default:
		return translateWorkflowError(err)
	}
}

// respondFiled returns the created ticket.
func (s *Server) respondFiled(c echo.Context, result workflows.FileTicketResult) error {
	body := TicketResponse{
		ID:         result.TicketID.String(),
		ProjectKey: result.Ticket.ContainerID,
		IssueKey:   result.Ticket.Key,
		IssueURL:   result.Ticket.URL,
		Title:      result.Ticket.Summary,
		Source:     result.Source,
		CreatedAt:  result.CreatedAt,
	}

	// A replay returns the original ticket with 200 rather than 201, and says
	// so in the body as well: nothing was created by this request.
	filed := FiledTicketResponse{TicketResponse: body, Created: !result.Replayed}
	if result.Replayed {
		return ok(c, http.StatusOK, filed)
	}

	c.Response().Header().Set(echo.HeaderLocation, s.publicURL+"/api/tickets/"+body.ID)
	return ok(c, http.StatusCreated, filed)
}

// --- The digest schedule ------------------------------------------------

// digestInterval is how often the NHI Blog Digest runs.
const digestInterval = 24 * time.Hour

// digestScheduleID names the schedule for one account's integration.
//
// The scope is in the ID so that schedules cannot collide between accounts, and
// so that a schedule can be found again from the same inputs -- which is what
// makes creation idempotent without keeping a local record of it.
func digestScheduleID(scope store.Scope, connectorType connector.Type) string {
	return fmt.Sprintf("%s/%s/%s/%s", blogDigestWorkflow, scope.OrgID, scope.AccountID, connectorType)
}

// ensureDigestSchedule creates the recurring digest for a newly connected
// integration.
//
// Temporal owns the schedule, so it survives restarts and needs no reconciling
// at boot. Failures here are logged and swallowed: the connection itself
// succeeded, and a missing digest is not worth failing that request over.
func (s *Server) ensureDigestSchedule(ctx context.Context, scope store.Scope, connectorType connector.Type) {
	if s.temporal == nil {
		return
	}

	id := digestScheduleID(scope, connectorType)
	handle := s.temporal.ScheduleClient().GetHandle(ctx, id)
	if _, err := handle.Describe(ctx); err == nil {
		return // already scheduled
	}

	_, err := s.temporal.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID: id,
		Spec: client.ScheduleSpec{
			Intervals: []client.ScheduleIntervalSpec{{Every: digestInterval}},
			// Jitter spreads accounts across the window rather than having
			// every schedule fire at once and stampede the provider.
			Jitter: 30 * time.Minute,
		},
		Action: &client.ScheduleWorkflowAction{
			// Every run of this account's digest, named for what it does.
			// Temporal appends the scheduled time, so runs stay distinct.
			ID:        blogDigestWorkflow + "-" + scope.AccountID.String(),
			Workflow:  workflows.BlogDigest,
			TaskQueue: s.taskQueue,
			Args: []any{workflows.DigestInput{
				OrgID:         scope.OrgID,
				AccountID:     scope.AccountID,
				ConnectorType: connectorType,
				TargetKey:     s.digestProjectKey,
				MaxPosts:      3,
			}},
		},
		// A run that overruns its 24-hour window must not stack up behind
		// itself: the next day's posts are picked up by the next run.
		Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
	})
	if err != nil && !errors.Is(err, temporal.ErrScheduleAlreadyRunning) {
		s.log.Warn("could not create digest schedule", "schedule_id", id, "error", err)
		return
	}
	s.log.Info("digest schedule created", "schedule_id", id, "every", digestInterval.String())
}

// removeDigestSchedule stops the digest when an integration is disconnected,
// since it has no credential left to run with.
func (s *Server) removeDigestSchedule(ctx context.Context, scope store.Scope, connectorType connector.Type) {
	if s.temporal == nil {
		return
	}

	id := digestScheduleID(scope, connectorType)
	if err := s.temporal.ScheduleClient().GetHandle(ctx, id).Delete(ctx); err != nil {
		s.log.Warn("could not delete digest schedule", "schedule_id", id, "error", err)
		return
	}
	s.log.Info("digest schedule deleted", "schedule_id", id)
}

// --- The digest ---------------------------------------------------------

// GetDigest reports the account's recurring blog digest.
func (s *Server) GetDigest(c echo.Context) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}

	digest, err := s.describeDigest(c.Request().Context(), principal.Scope)
	if err != nil {
		return err
	}
	return ok(c, http.StatusOK, digest)
}

// RunDigest starts a run now rather than waiting for the next scheduled one.
//
// Useful because the alternative is waiting a day. It is safe to press twice:
// the unique index on a catalogued post is what deduplicates filing, so a
// second run finds every post already catalogued and skips it.
func (s *Server) RunDigest(c echo.Context) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}
	if s.temporal == nil {
		return fault(http.StatusServiceUnavailable, codeQueueUnavailable,
			"The processing queue is unavailable. Try again shortly.")
	}

	ctx := c.Request().Context()
	id := digestScheduleID(principal.Scope, s.defaultConnectorType())

	err = s.temporal.ScheduleClient().GetHandle(ctx, id).Trigger(ctx, client.ScheduleTriggerOptions{
		// A run already going is not duplicated: the next day's posts are
		// picked up by the next run either way.
		Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
	})
	if err != nil {
		return fault(http.StatusNotFound, codeNotFound,
			"No blog digest is scheduled for this account. Connect an integration first.")
	}

	digest, err := s.describeDigest(ctx, principal.Scope)
	if err != nil {
		return err
	}
	return ok(c, http.StatusAccepted, digest)
}

// describeDigest reads the schedule, treating "there is none" as an answer
// rather than an error: an account with no integration has no digest, which is
// a state to report and not a failure.
func (s *Server) describeDigest(ctx context.Context, scope store.Scope) (DigestResponse, error) {
	out := DigestResponse{EveryHours: int(digestInterval.Hours()), ProjectKey: s.digestProjectKey}
	if s.temporal == nil {
		return out, nil
	}

	id := digestScheduleID(scope, s.defaultConnectorType())
	described, err := s.temporal.ScheduleClient().GetHandle(ctx, id).Describe(ctx)
	if err != nil {
		return out, nil
	}

	out.Scheduled = true
	out.Paused = described.Schedule.State.Paused
	if next := described.Info.NextActionTimes; len(next) > 0 {
		out.NextRunAt = &next[0]
	}
	if recent := described.Info.RecentActions; len(recent) > 0 {
		out.LastRunAt = &recent[len(recent)-1].ActualTime
	}
	return out, nil
}

// --- API keys -----------------------------------------------------------

// listAPIKeys returns the account's machine credentials.
//
// last_used_at is included deliberately: a key that has never been used, or has
// not been used in months, is a non-human identity worth retiring. That is this
// product's own subject matter applied to itself.
func (s *Server) ListAPIKeys(c echo.Context) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}

	keys, err := store.Read(s.pool).ListAPIKeys(c.Request().Context(), sqlcgen.ListAPIKeysParams{
		OrgID: principal.Scope.OrgID, AccountID: principal.Scope.AccountID,
	})
	if err != nil {
		return fmt.Errorf("list api keys: %w", err)
	}
	return ok(c, http.StatusOK, APIKeyListResponse{APIKeys: keys})
}

// createAPIKey mints a key and returns its only plaintext copy.
func (s *Server) CreateAPIKey(c echo.Context) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}

	var req CreateAPIKeyRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return fault(http.StatusBadRequest, codeMalformed, "The request body is not valid JSON.")
	}

	if err := req.Validate(); err != nil {
		return err
	}

	secret, summary, err := s.apiKeys.Issue(c.Request().Context(), principal, req.Name)
	if err != nil {
		return fmt.Errorf("create an api key: %w", err)
	}

	// The key ID identifies which credential was created. The secret is never
	// recorded anywhere, including here.
	s.audit(c, principal, store.ActionAPIKeyCreated, summary.KeyID,
		map[string]any{"name": summary.Name, "scopes": summary.Scopes})

	// The secret is returned exactly once. It is not recoverable afterwards,
	// because only its hash is stored.
	return ok(c, http.StatusCreated, CreatedAPIKeyResponse{
		APIKey: summary,
		Secret: secret,
		Notice: "Copy this key now. It will not be shown again.",
	})
}

// revokeAPIKey disables a key.
func (s *Server) RevokeAPIKey(c echo.Context, id uuid.UUID) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()

	var revoked int64
	if err := store.InTx(ctx, s.pool, func(q *sqlcgen.Queries) error {
		var err error
		revoked, err = q.RevokeAPIKey(ctx, sqlcgen.RevokeAPIKeyParams{
			ID: id, OrgID: principal.Scope.OrgID, AccountID: principal.Scope.AccountID,
		})
		return err
	}); err != nil {
		return fmt.Errorf("revoke api key %s: %w", id, err)
	}
	if revoked == 0 {
		// Unknown, already revoked, or another account's: all reported the
		// same, so the response cannot be used to discover that a key exists.
		return fault(http.StatusNotFound, codeNotFound,
			"This account has no API key with that identifier.")
	}

	s.audit(c, principal, store.ActionAPIKeyRevoked, id.String(), nil)
	return c.NoContent(http.StatusNoContent)
}

// --- Audit --------------------------------------------------------------

// listAuditEvents returns the account's recent security-relevant actions.
func (s *Server) ListAuditEvents(c echo.Context, params apigen.ListAuditEventsParams) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}

	limit, err := boundedParam("limit", params.Limit, defaultAuditEvents, maxAuditEvents)
	if err != nil {
		return err
	}

	events, err := store.Read(s.pool).RecentAuditEvents(c.Request().Context(), sqlcgen.RecentAuditEventsParams{
		OrgID:     principal.Scope.OrgID,
		AccountID: principal.Scope.AccountID,
		RowLimit:  int32(limit),
	})
	if err != nil {
		return fmt.Errorf("list audit events: %w", err)
	}
	return ok(c, http.StatusOK, AuditListResponse{Events: events})
}

// --- Public API ---------------------------------------------------------

// createFinding is the public endpoint for scanners and CI pipelines.
//
//	POST /api/v1/findings
//	Authorization: Bearer ih_...
//	Idempotency-Key: <optional>
//
// The organization and account come from the API key, exactly as they come from
// an access token on the browser API. An external system cannot name a tenancy
// any more than a browser can: the request type has no field for it.
func (s *Server) CreateFinding(c echo.Context, params apigen.CreateFindingParams) error {
	principal, err := principalOf(c)
	if err != nil {
		return err
	}

	var req CreateFindingRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return fault(http.StatusBadRequest, codeMalformed, "The request body is not valid JSON.")
	}

	if err := req.Validate(); err != nil {
		return err
	}

	key, err := idempotencyKey(value(params.IdempotencyKey))
	if err != nil {
		return err
	}

	// The caller's labels, plus our own marker so the ticket is identifiable in
	// the provider. The local record, not this label, is what the recent
	// tickets list trusts.
	labels := append([]string{"identityhub", "nhi-finding"}, req.Labels...)

	return s.fileFinding(c, principal, workflows.NewFinding{
		TargetKey:   req.ProjectKey,
		KindID:      req.IssueTypeID,
		Title:       req.Title,
		Description: req.Description,
		Labels:      labels,
	}, store.SourceAPI, key)
}
