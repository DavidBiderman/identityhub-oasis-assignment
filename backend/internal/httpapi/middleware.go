package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/dbiderman/identityhub/backend/internal/auth"
	"github.com/dbiderman/identityhub/backend/internal/store"
	"github.com/dbiderman/identityhub/backend/internal/store/sqlcgen"
)

// revocationLookupBudget bounds the sign-out check on every authenticated
// request. Redis is in-process-adjacent; a lookup that has not answered in this
// long has failed, and the request fails closed with it.
const revocationLookupBudget = 250 * time.Millisecond

// authenticate resolves a credential into a Principal and attaches it to the
// request context.
//
// This is the boundary where an external request becomes an internal one. On
// the way in, a request is untrusted bytes with a credential attached. On the
// way out, it carries a Principal whose organization and account came from the
// credential itself.
//
// Nothing downstream reads tenancy from the request. Handlers cannot, because
// the external request types have no field for it (see models.go), and
// repositories cannot, because they take a store.Scope that only a Principal
// can produce. A caller therefore has no way to express "act as another
// account", let alone to have it honoured -- the capability is absent from the
// type system, not merely rejected by a check.
func (s *Server) authenticate(resolver auth.TokenResolver) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			claims, err := resolver.Resolve(c.Request().Context(), c.Request())
			switch {
			case errors.Is(err, auth.ErrNoCredential):
				return fault(http.StatusUnauthorized, codeUnauthorized, "%s", credentialHint(resolver))
			case err != nil:
				// The resolver's own reason is deliberately not shown. A
				// rejected credential and an unknown one must fail
				// identically, or the response becomes a way to ask which
				// credentials exist. This is the one failure whose message is
				// chosen here rather than carried by the error.
				return fault(http.StatusUnauthorized, codeUnauthorized,
					"The credential presented was not accepted.")
			}

			// Backstop. A resolver rejects incomplete claims itself, because
			// a token that carries them is a bad credential rather than a bug.
			// Reaching here means a resolver failed to do that, which is a
			// programming error -- so it fails closed and says so, rather than
			// letting a half-empty scope reach a query.
			if err := claims.Validate(); err != nil {
				return fmt.Errorf("resolver %q returned incomplete claims: %w", resolver.Name(), err)
			}

			// A token that was signed out is refused for the rest of its own
			// lifetime. This is the one piece of state a self-contained token
			// cannot carry: it says when it expires, and nothing else can say
			// that it stopped being wanted before then.
			//
			// Only tokens carrying a jti are checked, because only they can be
			// named. An API key has a row of its own and is revoked there.
			if claims.TokenID != "" {
				// Its own deadline, like every other bounded call here.
				//
				// This runs on the hot path of every authenticated request. On
				// the raw request context, go-redis' own defaults apply -- a
				// few seconds per attempt, several attempts -- so a blackholed
				// Redis would hold each request for the better part of half a
				// minute before failing closed. It is a local lookup: if it has
				// not answered in this long it is not going to.
				lookup, cancel := context.WithTimeout(
					c.Request().Context(), revocationLookupBudget)
				defer cancel()

				revoked, err := s.revocations.IsRevoked(lookup, claims.TokenID)
				if err != nil {
					// Fail closed. Being unable to tell whether a credential
					// was revoked is not a reason to honour it.
					//
					// 503 rather than 500: nothing is wrong with the request,
					// a dependency is down, and trying again is the right
					// response. The reason is logged rather than returned --
					// which store is unreachable is not the caller's business.
					s.log.Error("cannot check token revocation",
						"request_id", c.Get("request_id"), "error", err)
					return fault(http.StatusServiceUnavailable, codeDependencyDown,
						"Credentials cannot be verified right now. Try again shortly.")
				}
				if revoked {
					return fault(http.StatusUnauthorized, codeUnauthorized,
						"This session was signed out. Sign in again to continue.")
				}
			}

			principal := auth.Principal{
				Scope:     claims.Scope,
				ActorID:   claims.ActorID,
				ActorType: claims.ActorType,
				Subject:   claims.Subject,
				Email:     claims.Email,
				Name:      claims.DisplayName,
				Roles:     claims.Roles,
				Scopes:    claims.Scopes,
				TokenID:   claims.TokenID,
				ExpiresAt: claims.ExpiresAt,
			}

			// A person gets a local row so that tickets and audit events can
			// name them. It is refreshed from the token on every request, so
			// a change at the provider is reflected here without a sync job.
			//
			// A failure is not fatal: the row is for display, and refusing the
			// request would make a naming concern into an outage.
			if principal.ActorType == auth.ActorUser {
				ctx := c.Request().Context()
				var userID uuid.UUID
				err := store.InTx(ctx, s.pool, func(q *sqlcgen.Queries) error {
					var err error
					userID, err = q.UpsertUser(ctx, sqlcgen.UpsertUserParams{
						OrgID:       claims.Scope.OrgID,
						AccountID:   claims.Scope.AccountID,
						Subject:     claims.Subject,
						Email:       claims.Email,
						DisplayName: claims.DisplayName,
					})
					return err
				})
				if err != nil {
					s.log.Warn("could not record the caller",
						"subject", claims.Subject, "error", err)
				} else {
					principal.ActorID = userID
				}
			}

			c.SetRequest(c.Request().WithContext(
				auth.WithPrincipal(c.Request().Context(), principal)))
			return next(c)
		}
	}
}

// credentialHint tells a caller what kind of credential the route expects,
// without revealing anything about credentials that exist.
func credentialHint(resolver auth.TokenResolver) string {
	if resolver.Name() == "api_key" {
		return "Provide an API key as: Authorization: Bearer ih_..."
	}
	return "Provide an access token as: Authorization: Bearer <token>"
}

// requireScope refuses a machine credential that lacks the named scope.
func requireScope(scope string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			principal, ok := auth.PrincipalFromContext(c.Request().Context())
			if !ok {
				return fault(http.StatusUnauthorized, codeUnauthorized,
					"This endpoint requires an API key.")
			}
			if !principal.HasScope(scope) {
				return fault(http.StatusForbidden, codeForbidden,
					"This API key does not carry the %s scope.", scope)
			}
			return next(c)
		}
	}
}

// requireAdmin refuses a caller the provider did not mark as an administrator.
//
// The role comes from the token. This application enforces it and never grants
// it: role assignment belongs to the identity provider.
func requireAdmin(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		principal, ok := auth.PrincipalFromContext(c.Request().Context())
		if !ok {
			return fault(http.StatusUnauthorized, codeUnauthorized,
				"This endpoint requires an access token.")
		}
		if !principal.IsAdmin() {
			return fault(http.StatusForbidden, codeForbidden,
				"Only an account administrator can change integration settings.")
		}
		return next(c)
	}
}

// principalOf returns the authenticated caller.
//
// Reaching a handler without a Principal means a route was mounted outside the
// authenticated group, which is a wiring mistake rather than an anonymous
// request. It is therefore an unclassified error, which is answered with a 500
// and logged -- not a 401, which would send someone off to check a credential
// that was never the problem.
func principalOf(c echo.Context) (auth.Principal, error) {
	principal, ok := auth.PrincipalFromContext(c.Request().Context())
	if !ok {
		return auth.Principal{}, fmt.Errorf("route %q is mounted outside the authenticated group", c.Path())
	}
	return principal, nil
}

// requestID attaches an identifier to every request and echoes it in the
// response, so a user reporting a failure and the log line describing it can be
// matched up.
func requestID(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		id := c.Request().Header.Get(echo.HeaderXRequestID)
		if id == "" {
			id = uuid.NewString()
		}
		c.Response().Header().Set(echo.HeaderXRequestID, id)
		c.Set("request_id", id)
		return next(c)
	}
}

// securityHeaders sets defaults appropriate for an API and an embedded SPA.
func securityHeaders(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		h := c.Response().Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// The SPA is served from this same origin, so it needs no external
		// script or style sources.
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		return next(c)
	}
}

// clientIP resolves the caller's address for audit events.
//
// Proxy headers are deliberately ignored: X-Forwarded-For is trivially forged
// unless every hop is trusted, and a forged value in an audit trail is worse
// than no value. A deployment behind a real proxy would configure Echo's
// trusted-proxy support rather than have this read the header blindly.
func clientIP(c echo.Context) *netip.Addr {
	host := c.Request().RemoteAddr
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		// inet has no zero value, so an unparseable address is stored as NULL
		// rather than as something that looks like an address.
		return nil
	}
	return &addr
}
