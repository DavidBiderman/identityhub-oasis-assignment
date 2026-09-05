package auth_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/dbiderman/identityhub/backend/internal/auth"
	"github.com/dbiderman/identityhub/backend/internal/store"
)

// This is the package that turns a bearer token into a tenancy, so the parts of
// it that need no infrastructure are tested without any: parsing a key, mapping
// claims, and the checks a token still needs once its signature is verified.

// The secret half of a key is base64url, whose alphabet contains the separator
// the key format uses. Splitting on every underscore would corrupt roughly one
// key in three.
//
// This asserts the parser directly. An earlier version drove it through the
// resolver with a nil database pool and inferred "did it parse?" from "did it
// panic?" — which depends on what a nil *pgxpool.Pool does when used, and that
// differs between pgx builds: on one it panics and the test passes, on another
// it blocks forever and `go test ./...` hangs. A pure function under test
// deserves to be called directly.
func TestAKeyIsSplitAtTheFirstTwoSeparatorsOnly(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		presented string
		keyID     string
		secret    string
		ok        bool
	}{
		"a secret containing underscores": {"ih_a1b2c3d4_sOme_secret_here", "a1b2c3d4", "sOme_secret_here", true},
		"a plain secret":                  {"ih_a1b2c3d4_secret", "a1b2c3d4", "secret", true},
		"surrounding whitespace":          {"  ih_a1b2c3d4_secret  ", "a1b2c3d4", "secret", true},
		"no underscores at all":           {"ihabcdef", "", "", false},
		"the wrong prefix":                {"gh_a1b2c3d4_secret", "", "", false},
		"an empty key id":                 {"ih__secret", "", "", false},
		"an empty secret":                 {"ih_a1b2c3d4_", "", "", false},
		"only two parts":                  {"ih_a1b2c3d4", "", "", false},
		"nothing at all":                  {"", "", "", false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			keyID, secret, ok := auth.SplitAPIKey(tc.presented)
			if ok != tc.ok || keyID != tc.keyID || secret != tc.secret {
				t.Errorf("got (%q, %q, %v), want (%q, %q, %v)",
					keyID, secret, ok, tc.keyID, tc.secret, tc.ok)
			}
		})
	}
}

func TestAMissingCredentialIsDistinctFromARejectedOne(t *testing.T) {
	t.Parallel()

	// A nil pool is safe here and only here: neither credential below parses,
	// so neither reaches a database call. Do not add a case with a well-formed
	// key -- what a nil *pgxpool.Pool does when used is undefined and differs
	// between pgx builds. A test needing one belongs in the gated tier.
	resolver := auth.NewAPIKeyResolver(auth.NewAPIKeys(nil))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/findings", nil)

	// Nothing presented is its own answer, so the API can say what kind of
	// credential the route wants. Anything else fails identically.
	if _, err := resolver.Resolve(req.Context(), req); err != auth.ErrNoCredential {
		t.Errorf("no header: got %v, want ErrNoCredential", err)
	}

	req.Header.Set("Authorization", "Bearer not-a-key")
	if _, err := resolver.Resolve(req.Context(), req); err != auth.ErrInvalidCredentials {
		t.Errorf("malformed key: got %v, want ErrInvalidCredentials", err)
	}
}

// Expiry is the only revocation this design has for a person's token: there is
// no session row to delete. A token that carries no expiry would therefore work
// forever, so it is refused rather than skipped.
func TestATokenIsCheckedBeyondItsSignature(t *testing.T) {
	t.Parallel()

	const issuer, audience = "https://issuer.test", "identityhub"
	tokens := auth.NewTokens([]byte("a-test-signing-key"), issuer, audience, time.Hour)

	base := map[string]any{
		"sub": "dev|ada", "org_id": uuid.NewString(), "account_id": uuid.NewString(),
		"iss": issuer, "aud": audience, "exp": time.Now().Add(time.Hour).Unix(),
	}

	cases := map[string]struct {
		change   func(map[string]any)
		accepted bool
	}{
		"a good token":        {func(map[string]any) {}, true},
		"no expiry at all":    {func(c map[string]any) { delete(c, "exp") }, false},
		"an expiry of zero":   {func(c map[string]any) { c["exp"] = 0 }, false},
		"already expired":     {func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }, false},
		"not valid yet":       {func(c map[string]any) { c["nbf"] = time.Now().Add(time.Hour).Unix() }, false},
		"another issuer":      {func(c map[string]any) { c["iss"] = "https://elsewhere.test" }, false},
		"another application": {func(c map[string]any) { c["aud"] = "someone-else" }, false},

		// RFC 7519 permits aud to be an array, and real providers emit one.
		// Reading it as a plain string -- which an earlier hand-rolled version
		// did -- would reject a valid token from Auth0 or Okta.
		"an audience list including ours": {
			func(c map[string]any) { c["aud"] = []string{"other-app", "identityhub"} }, true},
		"an audience list without ours": {
			func(c map[string]any) { c["aud"] = []string{"other-app", "third-app"} }, false},
		"no organization":      {func(c map[string]any) { delete(c, "org_id") }, false},
		"no account":           {func(c map[string]any) { delete(c, "account_id") }, false},
		"an unparseable org":   {func(c map[string]any) { c["org_id"] = "not-a-uuid" }, false},
		"the nil organization": {func(c map[string]any) { c["org_id"] = uuid.Nil.String() }, false},
		"no subject":           {func(c map[string]any) { delete(c, "sub") }, false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			claims := map[string]any{}
			for k, v := range base {
				claims[k] = v
			}
			tc.change(claims)

			req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
			req.Header.Set("Authorization", "Bearer "+signedToken(t, tokens, claims))

			_, err := tokens.Resolve(req.Context(), req)
			if (err == nil) != tc.accepted {
				t.Errorf("accepted = %v (%v), want %v", err == nil, err, tc.accepted)
			}
		})
	}
}

// A provider asserts roles from its own directory, and most of them mean
// nothing here. An unrecognised one is dropped rather than rejected, because
// refusing a token for carrying a role we do not use would break every caller
// whose provider has more roles than this application does.
func TestUnrecognisedRolesAreDroppedRatherThanRejected(t *testing.T) {
	t.Parallel()

	tokens := auth.NewTokens([]byte("a-test-signing-key"), "", "", time.Hour)
	token, _, err := tokens.Issue(auth.Identity{
		Subject:   "dev|ada",
		OrgID:     uuid.NewString(),
		AccountID: uuid.NewString(),
		// "billing-owner" is not a role this application knows. A provider
		// asserting it must not make the token unusable.
		Roles: []auth.Role{auth.RoleAdmin, "billing-owner", auth.RoleMember},
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	claims, err := tokens.Resolve(req.Context(), req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(claims.Roles) != 2 {
		t.Fatalf("roles = %v, want the two this application knows", claims.Roles)
	}
}

func TestAuthorizationIsDecidedFromTheClaims(t *testing.T) {
	t.Parallel()

	// ActorType is set on the people deliberately. HasScope branches on it,
	// and a Principal built without one takes the machine path by default --
	// so an assertion about a person that omits it passes for the wrong
	// reason, proving nothing about the branch it names.
	admin := auth.Principal{
		ActorType: store.ActorUser,
		Roles:     []auth.Role{auth.RoleAdmin, auth.RoleMember},
	}
	member := auth.Principal{
		ActorType: store.ActorUser,
		Roles:     []auth.Role{auth.RoleMember},
	}
	machine := auth.Principal{
		ActorType: store.ActorAPIKey,
		Scopes:    []string{auth.ScopeFindingsWrite},
	}

	switch {
	case !admin.IsAdmin():
		t.Error("an admin role did not make an administrator")
	case member.IsAdmin():
		t.Error("a member was treated as an administrator")
	case !machine.HasScope(auth.ScopeFindingsWrite):
		t.Error("a key with the scope was refused it")
	case machine.HasScope("connections:write"):
		t.Error("a key was granted a scope it does not carry")

	// A person is limited by roles, not by OAuth scopes: scopes exist to
	// narrow a machine credential. A person carrying none must still pass, or
	// there would be two authorization systems for one action.
	case !member.HasScope(auth.ScopeFindingsWrite):
		t.Error("a person was refused an action because their token carries no scopes")
	}
}

// signedToken builds a correctly signed token with arbitrary claims.
//
// It signs with the same key the resolver verifies against, so every case in
// the table above fails for the reason it names rather than for a bad
// signature. Forgery is covered separately, below.
func signedToken(t *testing.T, tokens *auth.Tokens, claims map[string]any) string {
	t.Helper()

	token, err := auth.SignForTest(tokens, claims)
	if err != nil {
		t.Fatalf("sign a test token: %v", err)
	}
	return token
}

// A signature is what makes every other check worth making.
//
// Without it the claims are a string the caller chose, and the tenancy read
// from them -- which is the whole isolation story -- would be whatever they
// typed. These are the three forgeries that matter, and each has to fail.
func TestAForgedTokenIsRefused(t *testing.T) {
	t.Parallel()

	const issuer, audience = "https://issuer.test", "identityhub"
	tokens := auth.NewTokens([]byte("the-real-signing-key"), issuer, audience, time.Hour)

	genuine, _, err := tokens.Issue(auth.Identity{
		Subject:   "dev|ada",
		OrgID:     uuid.NewString(),
		AccountID: uuid.NewString(),
		Roles:     []auth.Role{auth.RoleMember},
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	parts := strings.Split(genuine, ".")

	// Claims from the genuine token with the organization swapped: the forgery
	// somebody would actually attempt, because it is the one that would move
	// them into another tenant's data.
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(decoded, &claims); err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	claims["org_id"] = uuid.NewString()
	tampered, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("encode claims: %v", err)
	}
	enc := base64.RawURLEncoding.EncodeToString

	forgeries := map[string]string{
		"a token signed with another key": func() string {
			other := auth.NewTokens([]byte("a different key"), issuer, audience, time.Hour)
			token, _, err := other.Issue(auth.Identity{
				Subject: "dev|mallory", OrgID: uuid.NewString(), AccountID: uuid.NewString(),
			})
			if err != nil {
				t.Fatalf("issue with the other key: %v", err)
			}
			return token
		}(),
		// The classic JWT forgery: declare no algorithm and hope the verifier
		// believes the header. This one pins the algorithm instead of reading
		// it, so the header is not a thing a caller can negotiate.
		"an alg=none token": enc([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + parts[1] + ".",
		// Claims edited, signature left as it was.
		"tampered claims, original signature": parts[0] + "." + enc(tampered) + "." + parts[2],
		"no signature at all":                 parts[0] + "." + parts[1] + ".",
		"a signature from a different token":  parts[0] + "." + enc(tampered) + "." + enc([]byte("nope")),
	}

	for name, forged := range forgeries {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
			req.Header.Set("Authorization", "Bearer "+forged)

			if _, err := tokens.Resolve(req.Context(), req); err != auth.ErrInvalidCredentials {
				t.Fatalf("a forged token was accepted: %v", err)
			}
		})
	}

	// And the genuine one still works, so the test above is not passing because
	// everything is refused.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+genuine)
	if _, err := tokens.Resolve(req.Context(), req); err != nil {
		t.Fatalf("a genuine token was refused: %v", err)
	}
}

// Passwords are hashed with bcrypt, which salts each one itself.
func TestAPasswordIsHashedNotStored(t *testing.T) {
	t.Parallel()

	const password = "correct-horse-battery"

	first, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	second, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash again: %v", err)
	}

	switch {
	case strings.Contains(string(first), password):
		t.Error("the stored value contains the password")
	case string(first) == string(second):
		t.Error("the same password hashed to the same value; the salt is not per-password")
	}

	if err := auth.VerifyPassword(first, password); err != nil {
		t.Errorf("the right password was refused: %v", err)
	}
	if err := auth.VerifyPassword(first, password+"!"); err != auth.ErrBadCredentials {
		t.Errorf("a wrong password gave %v, want ErrBadCredentials", err)
	}
	// The second hash verifies the same password too, which is what proves the
	// two differ by salt rather than by being wrong.
	if err := auth.VerifyPassword(second, password); err != nil {
		t.Errorf("the second hash did not verify: %v", err)
	}
}

// The policy is a length floor and bcrypt's ceiling, and nothing else.
func TestThePasswordPolicyBoundsLengthOnly(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		password string
		accepted bool
	}{
		"long enough":             {"correct-horse", true},
		"a long passphrase":       {strings.Repeat("a", 72), true},
		"too short":               {"short", false},
		"empty":                   {"", false},
		"whitespace padding only": {"    abc    ", false},
		"past bcrypt's 72 bytes":  {strings.Repeat("a", 73), false},
		"no composition required": {"aaaaaaaaaaaa", true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := auth.CheckPasswordPolicy(tc.password)
			if (err == nil) != tc.accepted {
				t.Errorf("accepted = %v (%v), want %v", err == nil, err, tc.accepted)
			}
		})
	}
}

// fakeRedis is the three commands Revocations uses, and nothing else.
//
// Worth having because the interesting behaviour is not Redis's: it is that the
// TTL equals what is left of the token's life, that an already-expired token
// writes nothing, and that an unreachable store is an error rather than a
// "no". None of those need a server to check.
type fakeRedis struct {
	set  map[string]time.Duration
	fail error
}

func (f *fakeRedis) Set(_ context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(context.Background())
	if f.fail != nil {
		cmd.SetErr(f.fail)
		return cmd
	}
	if f.set == nil {
		f.set = map[string]time.Duration{}
	}
	f.set[key] = ttl
	cmd.SetVal("OK")
	return cmd
}

func (f *fakeRedis) Exists(_ context.Context, keys ...string) *redis.IntCmd {
	cmd := redis.NewIntCmd(context.Background())
	if f.fail != nil {
		cmd.SetErr(f.fail)
		return cmd
	}
	if _, found := f.set[keys[0]]; found {
		cmd.SetVal(1)
	} else {
		cmd.SetVal(0)
	}
	return cmd
}

func (f *fakeRedis) Ping(context.Context) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(context.Background())
	if f.fail != nil {
		cmd.SetErr(f.fail)
	} else {
		cmd.SetVal("PONG")
	}
	return cmd
}

// A revocation lives exactly as long as the token it revokes, and no longer.
//
// That equality is the whole reason this is a Redis key rather than a table
// row: past the token's own expiry the record has nothing left to say, and
// making the storage engine's TTL carry that removes the question of who
// deletes it and when.
func TestARevocationExpiresWithTheTokenItRevokes(t *testing.T) {
	t.Parallel()

	store := &fakeRedis{}
	revocations := auth.NewRevocations(store)
	ctx := context.Background()

	const remaining = 40 * time.Minute
	if err := revocations.Revoke(ctx, "jti-1", "dev|ada", time.Now().Add(remaining)); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	var ttl time.Duration
	for _, written := range store.set {
		ttl = written
	}
	// Within a second of the token's remaining life: the call itself takes time.
	if drift := remaining - ttl; drift < 0 || drift > time.Second {
		t.Errorf("ttl = %s, want the token's remaining %s", ttl, remaining)
	}

	revoked, err := revocations.IsRevoked(ctx, "jti-1")
	if err != nil || !revoked {
		t.Errorf("IsRevoked = %v (%v), want true", revoked, err)
	}
	if revoked, _ := revocations.IsRevoked(ctx, "jti-2"); revoked {
		t.Error("a token that was never signed out came back revoked")
	}
}

// Revoking a token that has already expired writes nothing. Refusing it is
// already what happens, so a record would be a key that exists to say what
// expiry already says.
func TestRevokingAnExpiredTokenWritesNothing(t *testing.T) {
	t.Parallel()

	store := &fakeRedis{}
	err := auth.NewRevocations(store).Revoke(
		context.Background(), "jti-old", "dev|ada", time.Now().Add(-time.Minute))

	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if len(store.set) != 0 {
		t.Errorf("wrote %d keys for an already-expired token, want none", len(store.set))
	}
}

// An unreachable store is an error, never a "not revoked".
//
// This is what lets the middleware fail closed. If this returned false on
// failure, a Redis outage would silently start honouring every signed-out
// token -- the worst possible failure mode, and an invisible one.
func TestAnUnreachableStoreIsAnErrorNotAnAnswer(t *testing.T) {
	t.Parallel()

	revocations := auth.NewRevocations(&fakeRedis{fail: errors.New("connection refused")})

	revoked, err := revocations.IsRevoked(context.Background(), "jti-1")
	if err == nil {
		t.Fatal("an unreachable store answered instead of failing")
	}
	if revoked {
		t.Error("a failed lookup reported the token as revoked, which is also wrong")
	}
	if err := revocations.Ping(context.Background()); err == nil {
		t.Error("Ping said the store was reachable")
	}
}
