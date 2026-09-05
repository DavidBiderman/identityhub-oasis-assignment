// Command worker runs the Temporal workflows and activities.
//
// It is a separate process from the API on purpose. The API is
// request/response, durable execution has a different failure model and a
// different scaling profile, and a worker that dies should not take the web
// tier with it.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/dbiderman/identityhub/backend/internal/blog"
	"github.com/dbiderman/identityhub/backend/internal/config"
	"github.com/dbiderman/identityhub/backend/internal/connections"
	"github.com/dbiderman/identityhub/backend/internal/connector"
	"github.com/dbiderman/identityhub/backend/internal/connector/jira"
	"github.com/dbiderman/identityhub/backend/internal/crypto"
	"github.com/dbiderman/identityhub/backend/internal/store"
	"github.com/dbiderman/identityhub/backend/internal/summarize"
	"github.com/dbiderman/identityhub/backend/internal/workflows"
)

// outboundTimeout bounds a call to a connected provider or the blog.
const outboundTimeout = 20 * time.Second

func main() {
	var cfg workerConfig
	config.MustLoad(context.Background(), "worker", &cfg)

	// Built here, before anything can fail. NewLogger installs the redacting
	// handler as the process default, so the failure line below cannot print a
	// value a connection error carried.
	log := config.NewLogger(cfg.Env)

	if err := run(cfg, log); err != nil {
		log.Error("worker failed", "error", err)
		os.Exit(1)
	}
}

func run(cfg workerConfig, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Shared resources, created once and handed down. Nothing further in
	// builds its own.
	pool, err := store.OpenPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	keeper, err := crypto.OpenKeeper(ctx, cfg.KeeperURL)
	if err != nil {
		return err
	}
	defer func() { _ = keeper.Close() }()

	httpClient := connector.NewHTTPClient(outboundTimeout)

	registry := connector.NewRegistry()
	jira.Register(registry, httpClient, jira.WithLogger(log))

	blogReader, err := blog.New(cfg.BlogFeedURL, blog.WithHTTPClient(httpClient))
	if err != nil {
		return err
	}

	temporalClient, err := client.Dial(client.Options{
		HostPort:  cfg.HostPort,
		Namespace: cfg.Namespace,
		Logger:    log,
	})
	if err != nil {
		return err
	}
	defer temporalClient.Close()

	summarizer, err := chooseSummarizer(cfg, log)
	if err != nil {
		return err
	}

	activities := &workflows.Activities{
		Pool:        pool,
		Connections: connections.New(pool, keeper, registry),
		Summarizer:  summarizer,
		Blog:        blogReader,
		Log:         log,
	}

	w := worker.New(temporalClient, workflows.TaskQueue, worker.Options{})
	w.RegisterWorkflow(workflows.FileTicket)
	w.RegisterWorkflow(workflows.BlogDigest)
	w.RegisterActivity(activities)

	// worker.Run takes an interface channel, so the signal context is bridged
	// onto one that closes when a shutdown signal arrives.
	shutdown := make(chan any)
	go func() {
		<-ctx.Done()
		close(shutdown)
	}()

	log.Info("worker starting", "task_queue", workflows.TaskQueue, "namespace", cfg.Namespace)
	return w.Run(shutdown)
}

// chooseSummarizer builds the summarizer the configuration asks for.
//
// Naming a provider and naming a provider that cannot be built are different
// situations. An operator who set SUMMARIZER_PROVIDER=anthropic asked for
// Claude; quietly handing back the offline summarizer would put "(Summarised
// without a model...)" on every digest ticket while the configuration says
// otherwise, which is a worse outcome than refusing to start.
//
// "auto" is the forgiving path, and the only one: it takes whichever key is
// present and falls back to offline when neither is, because the digest is a
// bonus feature and must not be the reason the stack does not start for someone
// with no model account.
func chooseSummarizer(cfg workerConfig, log *slog.Logger) (workflows.Summarizer, error) {
	switch cfg.SummarizerProvider {
	case summarizerOffline:
		log.Info("blog digest will summarize offline, by configuration")
		return summarize.Offline{}, nil

	case summarizerAnthropic:
		claude, err := summarize.NewClaude(cfg.AnthropicAPIKey)
		if err != nil {
			return nil, fmt.Errorf("SUMMARIZER_PROVIDER=anthropic: %w", err)
		}
		log.Info("blog digest will summarize with Claude")
		return claude, nil

	case summarizerOpenAI:
		client, err := summarize.NewOpenAI(cfg.OpenAIAPIKey, cfg.OpenAIModel)
		if err != nil {
			return nil, fmt.Errorf("SUMMARIZER_PROVIDER=openai: %w", err)
		}
		log.Info("blog digest will summarize with OpenAI", "model", cfg.OpenAIModel)
		return client, nil

	default: // auto
		switch {
		case cfg.AnthropicAPIKey != "":
			claude, err := summarize.NewClaude(cfg.AnthropicAPIKey)
			if err != nil {
				return nil, fmt.Errorf("ANTHROPIC_API_KEY is set but unusable: %w", err)
			}
			log.Info("blog digest will summarize with Claude")
			return claude, nil
		case cfg.OpenAIAPIKey != "":
			client, err := summarize.NewOpenAI(cfg.OpenAIAPIKey, cfg.OpenAIModel)
			if err != nil {
				return nil, fmt.Errorf("OPENAI_API_KEY is set but unusable: %w", err)
			}
			log.Info("blog digest will summarize with OpenAI", "model", cfg.OpenAIModel)
			return client, nil
		default:
			log.Info("no model API key configured; the blog digest will use the offline summarizer")
			return summarize.Offline{}, nil
		}
	}
}
