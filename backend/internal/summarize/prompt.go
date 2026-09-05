package summarize

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/dbiderman/identityhub/backend/internal/blog"
)

// The prompt and the bounds below are shared by every provider. Only the call
// differs, so a summary reads the same whichever model produced it and a
// change of provider is not a change of behaviour.

// maxTokens is deliberately small: the output is two or three sentences, and a
// larger ceiling would only mask a prompt that had stopped being followed.
const maxTokens = 1024

// maxPostChars bounds what is sent upstream. Post bodies come from a third
// party, and an unbounded input is both a cost and a reliability problem.
const maxPostChars = 24 << 10

// systemPrompt frames the summary for the audience that will read it: someone
// triaging a Jira ticket, not someone browsing a blog.
const systemPrompt = `You summarise blog posts for security engineers who will read the summary inside a Jira ticket.

Write two or three sentences of plain prose. Lead with what the post is actually about. Where the post is relevant to non-human identity — service accounts, API keys, machine credentials, workload identity — say how, concretely. Where it is not, do not manufacture a connection.

Do not use bullet points, headings, or markdown. Do not begin with "This post" or "The article". Do not add a preamble or closing remark: return only the summary itself.`

// ErrRefused reports that a model declined to summarise a post.
//
// It is surfaced rather than retried: a refusal is a decision about the
// content, and asking again produces the same answer. The digest skips that
// post and continues with the rest.
var ErrRefused = errors.New("the model declined to summarise this post")

// ErrEmptyResponse reports that a model returned no usable text.
var ErrEmptyResponse = errors.New("the model returned no text")

// prompt assembles a post for summarisation.
func prompt(p blog.Post) string {
	// Runes, not bytes: a byte cut can split a character, and what reaches the
	// model would carry a replacement character where the text was.
	body := strings.TrimSpace(p.Body)
	if utf8.RuneCountInString(body) > maxPostChars {
		body = string([]rune(body)[:maxPostChars])
	}

	var b strings.Builder
	b.WriteString("Title: ")
	b.WriteString(p.Title)
	b.WriteString("\nURL: ")
	b.WriteString(p.URL)
	if body != "" {
		b.WriteString("\n\n")
		b.WriteString(body)
	}
	return b.String()
}
