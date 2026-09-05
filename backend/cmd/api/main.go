// Command api serves the HTTP API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/jackc/pgx/v5/pgxpool"
	"gocloud.dev/secrets"

	"github.com/dbiderman/identityhub/backend/internal/auth"
	"github.com/dbiderman/identityhub/backend/internal/cache"
	"github.com/dbiderman/identityhub/backend/internal/config"
	"github.com/dbiderman/identityhub/backend/internal/connections"
	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/connector/jira"
	"github.com/dbiderman/identityhub/backend/internal/crypto"
	"github.com/dbiderman/identityhub/backend/internal/httpapi"
	"github.com/dbiderman/identityhub/backend/internal/store"
)

const (
	// shutdownGrace is how long in-flight requests get to finish.
	shutdownGrace = 15 * time.Second

	// outboundTimeout bounds a call to a connected provider. One client is
	// shared by every connector, so this is the process-wide ceiling.
	outboundTimeout = 20 * time.Second
)

func main() {
	if healthcheckRequested() {
		os.Exit(probeHealth())
	}

	// An unusable configuration is not something the process can work around,
	// and finding out at the first request rather than at startup turns a
	// deployment mistake into an outage. So it stops here, loudly.
	var cfg apiConfig
	config.MustLoad(context.Background(), "api", &cfg)

	// Built here, before anything can fail. NewLogger installs the redacting
	// handler as the process default, so the failure line below cannot print a
	// database URL or a key service address that a connection error carried.
	log := config.NewLogger(cfg.Env)

	if err := run(cfg, log); err != nil {
		log.Error("api failed", "error", err)
		os.Exit(1)
	}
}

// dependencies are the shared resources the process owns.
//
// Each is created once here and handed to whatever needs it. Nothing further
// down constructs a pool, a key service client or an HTTP client of its own:
// a shared resource created in a corner is one nobody can size, instrument or
// shut down.
type dependencies struct {
	log         *slog.Logger
	pool        *pgxpool.Pool
	keeper      *secrets.Keeper
	temporal    client.Client
	registry    *connector.Registry
	revocations *auth.Revocations
}

// open creates the shared resources, and a function that releases them in
// reverse order.
func open(ctx context.Context, cfg apiConfig, log *slog.Logger) (*dependencies, func(), error) {
	var closers []func()

	release := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}

	pool, err := store.OpenPool(ctx, cfg.DatabaseURL)
	if err != nil {
		release()
		return nil, nil, err
	}
	closers = append(closers, pool.Close)

	keeper, err := crypto.OpenKeeper(ctx, cfg.KeeperURL)
	if err != nil {
		release()
		return nil, nil, err
	}
	closers = append(closers, func() { _ = keeper.Close() })

	temporalClient, err := client.Dial(client.Options{
		HostPort:  cfg.HostPort,
		Namespace: cfg.Namespace,
		Logger:    log,
	})
	if err != nil {
		release()
		return nil, nil, err
	}
	closers = append(closers, temporalClient.Close)

	redisClient, err := cache.Open(ctx, cfg.RedisURL)
	if err != nil {
		release()
		return nil, nil, err
	}
	closers = append(closers, func() { _ = redisClient.Close() })

	// One HTTP client for every outbound provider call, so connection reuse
	// and the timeout are properties of the process rather than of whichever
	// package happened to construct one.
	httpClient := connector.NewHTTPClient(outboundTimeout)

	registry := connector.NewRegistry()
	jira.Register(registry, httpClient, jira.WithLogger(log))

	return &dependencies{
		log:         log,
		pool:        pool,
		keeper:      keeper,
		temporal:    temporalClient,
		registry:    registry,
		revocations: auth.NewRevocations(redisClient),
	}, release, nil
}

func run(cfg apiConfig, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	deps, release, err := open(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer release()

	// One key, both directions: this application issues access tokens and
	// verifies them, so a design where the two could disagree about the key or
	// the algorithm is a design with a hole in it.
	tokens := auth.NewTokens(
		[]byte(cfg.TokenSigningKey), cfg.TokenIssuer, cfg.TokenAudience, cfg.TokenTTL)

	server := httpapi.New(httpapi.Deps{
		Log:         deps.log,
		Pool:        deps.pool,
		APIKeys:     auth.NewAPIKeys(deps.pool),
		Revocations: deps.revocations,
		Connections: connections.New(deps.pool, deps.keeper, deps.registry),
		Registry:    deps.registry,
		Temporal:    deps.temporal,

		// The one seam. Replacing this resolver is the whole of moving to a
		// real identity provider: it would verify a signature against the
		// provider's JWKS instead of against our own key, and nothing
		// downstream of Claims would notice.
		UserResolver: tokens,
		SignIn:       auth.NewSignIn(deps.pool, tokens),

		PublicURL:        cfg.PublicURL,
		DigestProjectKey: cfg.DigestProjectKey,
		TokenIssuer:      cfg.TokenIssuer,
		TokenAudience:    cfg.TokenAudience,
	})

	routes, err := server.Routes()
	if err != nil {
		return err
	}

	// Every timeout, not just the header one. A client that sends complete
	// headers and then one body byte a minute is a parked goroutine and an
	// open socket that ReadHeaderTimeout has already been satisfied by, and
	// BodyLimit bounds size rather than duration. WriteTimeout has to exceed
	// the synchronous wait a filing request makes.
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           routes,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		deps.log.Info("api listening",
			"addr", cfg.Addr, "env", string(cfg.Env), "token_ttl", cfg.TokenTTL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		deps.log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
