package summarize

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/dbiderman/identityhub/backend/internal/blog"
)

// claudeModel is the Claude model the digest summarizes with.
//
// The SDK's typed constant rather than a string, so a typo is a compile error
// instead of a 404 discovered a day later inside a scheduled run, as a post
// that quietly failed to summarise. Its OpenAI sibling uses the same form.
//
// It is a floating alias, not a pinned snapshot: the digest asks for a short
// summary of a blog post, which does not need output identical across model
// revisions, and pinning would mean this quietly ages out. A deployment that
// needs reproducible output pins the dated identifier here instead.
const claudeModel = anthropic.ModelClaudeOpus5

// Claude summarises using the Anthropic API.
type Claude struct {
	client anthropic.Client
}

// NewClaude returns a summarizer using apiKey.
func NewClaude(apiKey string) (*Claude, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("an Anthropic API key is required")
	}
	return &Claude{client: anthropic.NewClient(option.WithAPIKey(apiKey))}, nil
}

// Summarize implements workflows.Summarizer.
func (c *Claude) Summarize(ctx context.Context, p blog.Post) (string, error) {
	resp, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     claudeModel,
		MaxTokens: maxTokens,
		System: []anthropic.TextBlockParam{{
			Text: systemPrompt,
		}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt(p))),
		},
	})
	if err != nil {
		return "", fmt.Errorf("ask Claude for a summary: %w", err)
	}

	// A refusal returns HTTP 200 with a refusal stop reason and no usable
	// content, so the stop reason has to be checked before reading the blocks.
	if resp.StopReason == anthropic.StopReasonRefusal {
		return "", fmt.Errorf("%w: %s", ErrRefused, resp.StopDetails.Category)
	}

	var out strings.Builder
	for _, block := range resp.Content {
		if text, ok := block.AsAny().(anthropic.TextBlock); ok {
			out.WriteString(text.Text)
		}
	}

	summary := strings.TrimSpace(out.String())
	if summary == "" {
		return "", ErrEmptyResponse
	}
	return truncate(summary), nil
}
