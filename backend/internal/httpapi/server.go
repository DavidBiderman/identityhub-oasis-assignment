package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"go.temporal.io/sdk/client"

	"github.com/dbiderman/identityhub/backend/api"
	"github.com/dbiderman/identityhub/backend/internal/auth"
	"github.com/dbiderman/identityhub/backend/internal/connections"
	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/httpapi/apigen"
	"github.com/dbiderman/identityhub/backend/internal/workflows"
)

// maxBodySize bounds every request body.
const maxBodySize = "256K"

// Server holds the HTTP layer's dependencies.
//
// It takes the few settings it needs as fields rather than a whole
// configuration struct, so that this layer cannot start depending on a value
// some other binary reads.
type Server struct {
	log         *slog.Logger
	pool        *pgxpool.Pool
	apiKeys     *auth.APIKeys
	signIn      *auth.SignIn
	revocations *auth.Revocations
	connections *connections.Service
	registry    *connector.Registry
	temporal    client.Client

	// userResolver authenticates browser callers from their access token. It
	// is the one seam between this application and an identity provider:
	// swapping the implementation is the whole of adopting a real one, and
	// nothing downstream of Claims changes.
	userResolver   auth.TokenResolver
	apiKeyResolver auth.TokenResolver

	// taskQueue is the Temporal queue workflows are started on. It is a field
	// rather than a constant so that a test can isolate itself from any worker
	// that happens to be running against the same Temporal namespace.
	taskQueue string

	publicURL        string
	digestProjectKey string

	// tokenIssuer and tokenAudience are the values the resolver checks, and
	// that the development identity endpoint stamps onto the tokens it mints.
	tokenIssuer   string
	tokenAudience string
}

// Deps are the collaborators a Server needs.
type Deps struct {
	Log         *slog.Logger
	Pool        *pgxpool.Pool
	APIKeys     *auth.APIKeys
	SignIn      *auth.SignIn
	Revocations *auth.Revocations
	Connections *connections.Service
	Registry    *connector.Registry
	Temporal    client.Client
	// UserResolver overrides the default session resolver.
	UserResolver auth.TokenResolver
	// TaskQueue overrides the default Temporal task queue.
	TaskQueue string

	PublicURL        string
	DigestProjectKey string

	TokenIssuer   string
	TokenAudience string
}

// New builds a Server.
func New(d Deps) *Server {
	s := &Server{
		log:            d.Log,
		pool:           d.Pool,
		apiKeys:        d.APIKeys,
		signIn:         d.SignIn,
		revocations:    d.Revocations,
		connections:    d.Connections,
		registry:       d.Registry,
		temporal:       d.Temporal,
		userResolver:   d.UserResolver,
		apiKeyResolver: auth.NewAPIKeyResolver(d.APIKeys),
		taskQueue:      d.TaskQueue,

		publicURL:        d.PublicURL,
		digestProjectKey: d.DigestProjectKey,

		tokenIssuer:   d.TokenIssuer,
		tokenAudience: d.TokenAudience,
	}
	if s.taskQueue == "" {
		s.taskQueue = workflows.TaskQueue
	}
	return s
}

// Routes returns the configured Echo instance.
//
// Every route comes from api/openapi.yaml. Nothing is registered here by hand:
// the generated RegisterHandlers walks the document, and Server implements the
// generated ServerInterface, so an operation added to the spec and not
// implemented is a compile error rather than a 404 somebody finds later.
//
// This process serves the API and nothing else. The single-page application is
// a separate build served by the reverse proxy in front of both, which keeps
// the Go binary independent of whether the frontend has been built.
func (s *Server) Routes() (*echo.Echo, error) {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.HTTPErrorHandler = s.render

	e.Use(requestID)
	e.Use(securityHeaders)
	e.Use(middleware.Recover())
	e.Use(s.accessLog)

	// One bound for every route, rather than one per handler that reads a
	// body. Descriptions are free text so the limit is generous, but an
	// unbounded read is not something to remember to add.
	e.Use(middleware.BodyLimit(maxBodySize))

	guards, err := s.guards()
	if err != nil {
		return nil, err
	}
	apigen.RegisterHandlersWithOptions(e, s, apigen.RegisterHandlersOptions{
		OperationMiddlewares: guards,
	})

	// The spec is served from the process that implements it, so what a reader
	// tries against /docs is the document this binary was built from.
	//
	// It requires a signed-in caller. This document lists every endpoint, every
	// field and every constraint of an API that manages credentials; publishing
	// it to anyone who asks hands an attacker the map before they start. A
	// customer's own people can read it, which is who it is for.
	//
	// The guard is applied here rather than in the document's security section
	// because this route is not one of the document's own operations -- it is
	// how the document is fetched, and a spec cannot describe its own delivery.
	e.GET("/openapi.yaml", s.openAPIDocument, s.authenticate(s.userResolver))
	return e, nil
}

// guards builds each operation's middleware from what the document says about
// it.
//
// The security section of an operation is the authorization policy, and this
// reads it rather than restating it: accessToken means a person's token,
// accessToken with the admin scope adds the administrator check, apiKey means
// the public API and its scopes. An operation with security: [] is public.
//
// Declaring it in the spec and enforcing it from a hand-written route table
// would be two statements of one rule, and the pair would drift. Here the
// document is the rule.
func (s *Server) guards() (map[string][]echo.MiddlewareFunc, error) {
	// Loaded from the document itself rather than from the copy oapi-codegen
	// embeds, for two reasons. The embedded copy normalises operation ids to
	// Go names, and the registration map is keyed by the id as written. And
	// the bytes read here are the bytes served at /openapi.yaml, so the policy
	// and the published contract cannot be different documents.
	document, err := openapi3.NewLoader().LoadFromData(api.Spec)
	if err != nil {
		return nil, fmt.Errorf("read the OpenAPI document: %w", err)
	}

	guards := make(map[string][]echo.MiddlewareFunc)
	for _, item := range document.Paths.Map() {
		for _, op := range item.Operations() {
			// An operation with no security section inherits the document's;
			// one with an empty list is deliberately public.
			requirements := document.Security
			if op.Security != nil {
				requirements = *op.Security
			}
			if len(requirements) == 0 {
				continue
			}

			// A list of requirements is OR, not AND. OpenAPI reads
			//
			//     security:
			//       - accessToken: []
			//       - apiKey: []
			//
			// as "either of these will do", and an earlier version of this
			// appended every requirement's middleware into one chain -- which
			// made it AND, and made the operation unreachable: one Authorization
			// header cannot satisfy two authenticators, so a person's token
			// cleared the first and was refused by the second.
			//
			// Alternatives have to be tried in turn, so each becomes one
			// middleware that runs its own chain and moves on to the next only
			// if that chain refuses.
			alternatives := make([]echo.MiddlewareFunc, 0, len(requirements))
			for _, requirement := range requirements {
				chain := make([]echo.MiddlewareFunc, 0, len(requirement))
				for scheme, scopes := range requirement {
					middlewares, err := s.guard(scheme, scopes)
					if err != nil {
						return nil, fmt.Errorf("operation %q: %w", op.OperationID, err)
					}
					chain = append(chain, middlewares...)
				}
				alternatives = append(alternatives, apply(chain))
			}
			guards[op.OperationID] = []echo.MiddlewareFunc{firstThatAccepts(alternatives)}
		}
	}
	return guards, nil
}

// apply folds a chain of middleware into one.
//
// Composed back to front so the first in the slice is the outermost, which is
// the order they were written in.
func apply(chain []echo.MiddlewareFunc) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		for i := len(chain) - 1; i >= 0; i-- {
			next = chain[i](next)
		}
		return next
	}
}

// firstThatAccepts tries each alternative in turn and runs the handler behind
// the first one that lets the request through.
//
// An alternative "accepts" by reaching the handler. That is the only signal
// available: middleware either calls next or returns an error, and there is no
// third answer to inspect. So the handler is wrapped in a sentinel that records
// having been reached, and an alternative that returns without reaching it is
// treated as a refusal and the next one is tried.
//
// The last refusal is the one reported, because with one credential presented
// only one alternative can have got past parsing it -- and its error is the one
// that describes what was actually wrong. A caller presenting nothing gets the
// last requirement's "no credential" answer, which names a credential the route
// accepts.
func firstThatAccepts(alternatives []echo.MiddlewareFunc) echo.MiddlewareFunc {
	if len(alternatives) == 1 {
		return alternatives[0]
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			var refusal error
			for _, alternative := range alternatives {
				accepted := false
				sentinel := func(c echo.Context) error {
					accepted = true
					return next(c)
				}
				if err := alternative(sentinel)(c); err != nil && !accepted {
					refusal = err
					continue
				} else if err != nil {
					// The handler itself failed, after being authenticated.
					// That is not a refusal and must not be retried.
					return err
				}
				return nil
			}
			return refusal
		}
	}
}

// guard turns one security requirement into middleware.
func (s *Server) guard(scheme string, scopes []string) ([]echo.MiddlewareFunc, error) {
	switch scheme {
	case "accessToken":
		middlewares := []echo.MiddlewareFunc{s.authenticate(s.userResolver)}
		for _, scope := range scopes {
			if scope != string(auth.RoleAdmin) {
				return nil, fmt.Errorf("unknown role %q on the accessToken scheme", scope)
			}
			middlewares = append(middlewares, requireAdmin)
		}
		return middlewares, nil

	case "apiKey":
		middlewares := []echo.MiddlewareFunc{s.authenticate(s.apiKeyResolver)}
		for _, scope := range scopes {
			middlewares = append(middlewares, requireScope(scope))
		}
		return middlewares, nil

	default:
		return nil, fmt.Errorf("unknown security scheme %q", scheme)
	}
}

// openAPIDocument serves the specification this binary implements.
func (s *Server) openAPIDocument(c echo.Context) error {
	return c.Blob(http.StatusOK, "application/yaml", api.Spec)
}

// health reports that the process is running.
func (s *Server) Health(c echo.Context) error {
	return ok(c, http.StatusOK, map[string]string{"status": "ok"})
}

// ready reports whether dependencies are reachable, so an orchestrator can hold
// traffic until they are.
func (s *Server) Ready(c echo.Context) error {
	// Each dependency gets its own deadline rather than a share of one.
	//
	// They used to run in sequence against a single two-second context, so the
	// first one to hang consumed the budget and everything after it was
	// reported unavailable as well. A readiness page that blames three
	// services when one is down is worse than no readiness page: it sends
	// whoever is on call to the wrong place.
	probe := func(name string, check func(context.Context) error) (string, bool) {
		ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
		defer cancel()

		if err := check(ctx); err != nil {
			s.log.Warn("dependency is unavailable", "dependency", name, "error", err)
			return "unavailable", false
		}
		return "ok", true
	}

	checks := map[string]string{}
	ready := true
	for _, dependency := range []struct {
		name  string
		check func(context.Context) error
	}{
		{"database", s.pool.Ping},
		// Redis is a hard dependency, not a cache: the revocation check fails
		// closed, so a process that cannot reach it can authenticate nobody.
		// Reporting ready would send it traffic it must refuse.
		{"redis", s.revocations.Ping},
		{"temporal", func(ctx context.Context) error {
			if s.temporal == nil {
				return nil
			}
			_, err := s.temporal.CheckHealth(ctx, &client.CheckHealthRequest{})
			return err
		}},
	} {
		state, healthy := probe(dependency.name, dependency.check)
		checks[dependency.name] = state
		ready = ready && healthy
	}

	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	return c.JSON(status, map[string]any{"ready": ready, "checks": checks})
}

// accessLog records one line per request, without secrets.
func (s *Server) accessLog(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		start := time.Now()
		err := next(c)

		status := c.Response().Status
		if err != nil {
			var he *echo.HTTPError
			if errors.As(err, &he) {
				status = he.Code
			} else {
				status = http.StatusInternalServerError
			}
		}

		// The path is logged, never the query string or body: both can carry
		// values a user typed, and neither is worth retaining.
		s.log.Info("request",
			"method", c.Request().Method,
			"path", c.Path(),
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", c.Get("request_id"),
		)
		return err
	}
}
