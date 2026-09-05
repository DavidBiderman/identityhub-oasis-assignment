package summarize_test

import (
	"github.com/dbiderman/identityhub/backend/internal/summarize"
	"github.com/dbiderman/identityhub/backend/internal/workflows"
)

// Compile-time proof that every summarizer satisfies the port the workflow
// declares.
//
// In a test, not in the package. The assertion is worth having -- a signature
// that drifts fails here rather than at wiring time in main -- but it is not
// worth an import that would make a text summariser depend on the durable
// execution engine. A test binary may depend on anything; the package may not.
var (
	_ workflows.Summarizer = summarize.Offline{}
	_ workflows.Summarizer = (*summarize.Claude)(nil)
	_ workflows.Summarizer = (*summarize.OpenAI)(nil)
)
