package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/dbiderman/identityhub/backend/internal/config"
)

// config is everything the worker process reads from the environment.
//
// Note what is absent: no listen address, no session lifetime, no cookie
// settings. The worker serves no HTTP and issues no sessions, so it does not
// ask for the values that would configure them.
type workerConfig struct {
	config.Core
	config.Secrets
	config.Temporal

	BlogFeedURL string `env:"BLOG_FEED_URL,default=https://www.oasis.security/blog"`

	// SummarizerProvider selects which model writes the digest summaries.
	//
	// "auto" uses whichever key is configured, preferring Anthropic, and falls
	// back to an offline summarizer when neither is. That keeps the stack
	// runnable with no account and no network access, which matters because
	// the digest is a bonus feature and must not be the reason nothing starts.
	SummarizerProvider string `env:"SUMMARIZER_PROVIDER,default=auto"`

	AnthropicAPIKey string `env:"ANTHROPIC_API_KEY"`
	OpenAIAPIKey    string `env:"OPENAI_API_KEY"`
	OpenAIModel     string `env:"OPENAI_MODEL"`
}

// Summarizer providers.
const (
	summarizerAuto      = "auto"
	summarizerAnthropic = "anthropic"
	summarizerOpenAI    = "openai"
	summarizerOffline   = "offline"
)

// Validate reports every problem at once.
func (c workerConfig) Validate() error {
	errs := []error{c.Core.Validate(), c.Secrets.Validate(c.Env), c.Temporal.Validate()}

	parsed, err := url.Parse(strings.TrimSpace(c.BlogFeedURL))
	switch {
	case err != nil || parsed.Host == "":
		errs = append(errs, fmt.Errorf("BLOG_FEED_URL must be a valid URL"))
	case parsed.Scheme != "https":
		// The digest runs unattended, so anything on the path between here and
		// the feed would get to choose what tickets are filed.
		errs = append(errs, fmt.Errorf("BLOG_FEED_URL must use https"))
	}

	switch c.SummarizerProvider {
	case summarizerAuto, summarizerOffline:
	case summarizerAnthropic:
		if strings.TrimSpace(c.AnthropicAPIKey) == "" {
			errs = append(errs, fmt.Errorf(
				"ANTHROPIC_API_KEY is required when SUMMARIZER_PROVIDER=anthropic"))
		}
	case summarizerOpenAI:
		if strings.TrimSpace(c.OpenAIAPIKey) == "" {
			errs = append(errs, fmt.Errorf(
				"OPENAI_API_KEY is required when SUMMARIZER_PROVIDER=openai"))
		}
	default:
		errs = append(errs, fmt.Errorf(
			"SUMMARIZER_PROVIDER must be one of %q, %q, %q or %q, got %q",
			summarizerAuto, summarizerAnthropic, summarizerOpenAI, summarizerOffline,
			c.SummarizerProvider))
	}

	return errors.Join(errs...)
}
