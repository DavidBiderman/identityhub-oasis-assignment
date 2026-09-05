package connector_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dbiderman/identityhub/backend/internal/connector"
)

// An account administrator types the address a connector fetches. Nothing stops
// them typing one inside our own network, so the client has to.
func TestTheOutboundClientRefusesPrivateAddresses(t *testing.T) {
	t.Parallel()

	// A real server on loopback, so what is being tested is the refusal and not
	// a connection that would have failed anyway.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := connector.NewHTTPClient(5 * time.Second)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a loopback address was dialled; an internal port scan is possible")
	}
	if !strings.Contains(err.Error(), "not a public address") {
		t.Errorf("error = %v, want it to name the reason", err)
	}
}

// The guard must not refuse the addresses the product actually talks to.
func TestTheOutboundClientAllowsPublicAddresses(t *testing.T) {
	t.Parallel()

	client := connector.NewHTTPClient(5 * time.Second)

	// A public address that is not routed anywhere: the dial is attempted and
	// times out, which is the point -- the guard let it through.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://198.51.100.1", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if _, err := client.Do(req); err != nil && strings.Contains(err.Error(), "not a public address") {
		t.Errorf("a public address was refused: %v", err)
	}
}

// A proxy would route around the guard entirely: the transport dials the proxy,
// which is public and therefore allowed, and sends CONNECT to the private
// address. Control never sees the target. This pins the transport's own setting
// rather than the environment, because the environment is the attack.
func TestTheOutboundClientIgnoresAProxy(t *testing.T) {
	// Serial, unlike its neighbours: t.Setenv and t.Parallel cannot both apply
	// to one test, because an environment variable is process-wide and a
	// parallel test cannot own one.
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:3128")

	transport, ok := connector.NewHTTPClient(time.Second).Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", transport)
	}
	if transport.Proxy != nil {
		t.Error("the outbound client would egress through a proxy, which bypasses the dialer guard")
	}
}
