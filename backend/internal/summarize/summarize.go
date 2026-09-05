// Package summarize turns a blog post into a few sentences for a ticket.
//
// Three implementations satisfy the same port. Claude and OpenAI are the real
// ones -- two rather than one because the port is worth demonstrating, and a
// second provider is a small file. Offline is deterministic and needs no
// network or API key, so the test suite and a network-isolated
// `docker compose up` still work: the digest is a demonstration feature and
// should not be the reason the stack fails to start.
package summarize

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dbiderman/identityhub/backend/internal/blog"
)

// maxSummaryChars bounds a summary before it reaches a ticket description.
const maxSummaryChars = 1200

// Offline produces a summary without calling a model.
//
// It is extractive rather than generative: it returns the post's own
// description, trimmed. That is honest about what it is -- a stand-in -- rather
// than pretending to be a model, and it keeps the digest runnable with no
// credentials configured.
type Offline struct{}

// Summarize implements workflows.Summarizer.
func (Offline) Summarize(_ context.Context, p blog.Post) (string, error) {
	body := strings.TrimSpace(p.Body)
	if body == "" {
		return fmt.Sprintf("No summary is available for %q. Open the post for details.", p.Title), nil
	}
	return truncate(body) + "\n\n(Summarised without a model — no model API key is configured.)", nil
}

// truncate bounds a summary at a sentence boundary where it can.
//
// Counted in runes: the input is prose from a blog, and cutting a multi-byte
// character in half produces invalid UTF-8 that json.Marshal turns into U+FFFD.
func truncate(s string) string {
	if utf8.RuneCountInString(s) <= maxSummaryChars {
		return s
	}
	cut := string([]rune(s)[:maxSummaryChars])
	if idx := strings.LastIndexAny(cut, ".!?"); idx > len(cut)/2 {
		return cut[:idx+1]
	}
	return strings.TrimSpace(cut) + "…"
}
