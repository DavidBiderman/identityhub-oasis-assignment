package main

import (
	"context"
	"flag"
	"net/http"
	"strings"
	"time"

	"github.com/sethvargo/go-envconfig"
)

// healthcheckProbeTimeout bounds the local probe.
const healthcheckProbeTimeout = 3 * time.Second

// healthcheckRequested reports whether the process was invoked as a health
// probe rather than as the server.
//
// This exists so that the runtime image needs no curl or wget and can stay a
// distroless base with nothing in it but the binary and CA certificates.
func healthcheckRequested() bool {
	probe := flag.Bool("healthcheck", false, "probe the local health endpoint and exit")
	flag.Parse()
	return *probe
}

// probeConfig is the one value the health probe needs.
//
// It is read separately from the server's configuration, because a probe must
// work without a database or a key service: requiring those would make an
// unhealthy dependency look like an unhealthy process.
type probeConfig struct {
	Addr string `env:"APP_ADDR,default=:8080"`
}

// probeHealth returns 0 when the local server reports healthy.
func probeHealth() int {
	var cfg probeConfig
	if err := envconfig.Process(context.Background(), &cfg); err != nil {
		return 1
	}

	addr := cfg.Addr
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}

	client := &http.Client{Timeout: healthcheckProbeTimeout}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		return 1
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
