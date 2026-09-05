package connector

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// NewHTTPClient returns the client connectors use for outbound requests.
//
// A connector's address comes from a customer: an account administrator types
// a site URL, this process fetches it, and what comes back is shown in the UI
// and stored. That makes an unguarded client a way to ask this application to
// probe its own network -- to tell a reachable internal port from an
// unreachable one by the status it reports, and to read back whatever a 2xx
// JSON body contained.
//
// So the dialer refuses any address that is not routable on the public
// internet. The check is in Control rather than in URL validation because
// Control runs after DNS resolution, on the address actually being connected
// to: a hostname that resolves to a public address at validation time and to
// 169.254.169.254 a second later is refused at the point it matters. It also
// covers redirects, which URL validation never sees.
//
// A process builds one of these and hands it to every connector; a test
// supplies its own client, pointed at its own stub.
func NewHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			return checkPublic(address)
		},
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}

	// No proxy, which is part of the guard rather than a preference.
	//
	// The clone inherits ProxyFromEnvironment. With HTTPS_PROXY set -- a
	// corporate egress proxy, a mesh sidecar, a debugging leftover -- the
	// transport dials the proxy and sends CONNECT to the target, so Control
	// checks the proxy's address and never sees where the request is going.
	// A site URL of https://10.0.0.7 would then be reached through a proxy
	// that is itself perfectly public. These connectors talk to provider APIs
	// on the public internet and have no reason to egress through anything.
	transport.Proxy = nil
	return &http.Client{Timeout: timeout, Transport: transport}
}

// cgnat is the shared address space of RFC 6598. Cloud provider metadata and
// several managed networks live in it, and netip has no predicate for it.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// checkPublic refuses an address that is not routable on the public internet.
func checkPublic(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("refusing to dial %q: it is not an address", address)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		// Control is called with a resolved address, so this cannot be a
		// hostname. Refuse rather than guess.
		return fmt.Errorf("refusing to dial %q: it is not an IP address", host)
	}

	ip = ip.Unmap()
	switch {
	case ip.IsLoopback(), ip.IsPrivate(), ip.IsUnspecified(),
		ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(), ip.IsMulticast(),
		cgnat.Contains(ip):
		return fmt.Errorf("refusing to dial %s: it is not a public address", ip)
	}
	return nil
}
