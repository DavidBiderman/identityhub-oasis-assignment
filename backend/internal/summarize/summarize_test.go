package summarize_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dbiderman/identityhub/backend/internal/blog"
	"github.com/dbiderman/identityhub/backend/internal/summarize"
)

func TestOfflineIsDeterministic(t *testing.T) {
	t.Parallel()

	post := blog.Post{
		Title: "When Shai-Hulud Steals Your Keys",
		URL:   "https://www.oasis.security/blog/shai-hulud",
		Body:  "The worm steals credentials, not code.",
	}

	first, err := summarize.Offline{}.Summarize(context.Background(), post)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	second, _ := summarize.Offline{}.Summarize(context.Background(), post)

	if first != second {
		t.Error("offline summarizer is not deterministic")
	}
	if !strings.Contains(first, "steals credentials") {
		t.Errorf("summary lost the post body: %q", first)
	}
	// It must be obvious in the ticket that no model was involved.
	if !strings.Contains(first, "without a model") {
		t.Errorf("summary does not disclose that no model was used: %q", first)
	}
}

func TestOfflineHandlesAnEmptyBody(t *testing.T) {
	t.Parallel()

	out, err := summarize.Offline{}.Summarize(context.Background(),
		blog.Post{Title: "Untitled", URL: "https://example.test/x"})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if out == "" {
		t.Error("empty body produced an empty summary")
	}
}

// A summary reaches a ticket description, so it must be bounded.
func TestOfflineBoundsLongBodies(t *testing.T) {
	t.Parallel()

	out, err := summarize.Offline{}.Summarize(context.Background(), blog.Post{
		Title: "Long", URL: "https://example.test/x",
		Body: strings.Repeat("Sentence about non-human identity. ", 200),
	})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if len(out) > 1500 {
		t.Errorf("summary is %d chars, want it bounded", len(out))
	}
}

// Both providers share one prompt and one set of bounds, so a summary reads the
// same whichever produced it. These assert the parts that do not need a network.
func TestProvidersRequireCredentials(t *testing.T) {
	t.Parallel()

	if _, err := summarize.NewClaude("  "); err == nil {
		t.Error("Claude accepted a blank API key")
	}
	if _, err := summarize.NewOpenAI("  ", ""); err == nil {
		t.Error("OpenAI accepted a blank API key")
	}
}

func TestOpenAIDefaultsItsModel(t *testing.T) {
	t.Parallel()

	if _, err := summarize.NewOpenAI("sk-test", ""); err != nil {
		t.Fatalf("an empty model should select a default: %v", err)
	}
	if _, err := summarize.NewOpenAI("sk-test", "gpt-5.2-mini"); err != nil {
		t.Fatalf("an explicit model was rejected: %v", err)
	}
}
