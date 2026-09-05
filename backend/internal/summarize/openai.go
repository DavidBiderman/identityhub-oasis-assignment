package summarize

import (
	"context"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	"github.com/dbiderman/identityhub/backend/internal/blog"
)

// defaultOpenAIModel is used when none is configured.
const defaultOpenAIModel = shared.ChatModelGPT5_2

// OpenAI summarises using the OpenAI API.
//
// It exists alongside Claude because which model a deployment may use is often
// decided by procurement rather than engineering. Both satisfy the same port,
// share the same prompt and the same bounds, so a summary reads the same
// whichever produced it, and switching is configuration.
type OpenAI struct {
	client openai.Client
	model  shared.ChatModel
}

// NewOpenAI returns a summarizer using apiKey. An empty model selects a default.
func NewOpenAI(apiKey, model string) (*OpenAI, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("an OpenAI API key is required")
	}

	chosen := shared.ChatModel(strings.TrimSpace(model))
	if chosen == "" {
		chosen = defaultOpenAIModel
	}

	return &OpenAI{
		client: openai.NewClient(option.WithAPIKey(apiKey)),
		model:  chosen,
	}, nil
}

// Summarize implements workflows.Summarizer.
func (o *OpenAI) Summarize(ctx context.Context, p blog.Post) (string, error) {
	resp, err := o.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:     o.model,
		MaxTokens: openai.Int(maxTokens),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(prompt(p)),
		},
	})
	if err != nil {
		return "", fmt.Errorf("ask OpenAI for a summary: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", ErrEmptyResponse
	}

	choice := resp.Choices[0]

	// A refusal arrives as a populated Refusal field rather than an error, so
	// it has to be checked before the content is read.
	if refusal := strings.TrimSpace(choice.Message.Refusal); refusal != "" {
		return "", fmt.Errorf("%w: %s", ErrRefused, refusal)
	}
	// content_filter is the other way a request can be declined.
	if choice.FinishReason == "content_filter" {
		return "", fmt.Errorf("%w: the request was filtered", ErrRefused)
	}

	summary := strings.TrimSpace(choice.Message.Content)
	if summary == "" {
		return "", ErrEmptyResponse
	}
	return truncate(summary), nil
}
