package blog_test

import (
	"github.com/dbiderman/identityhub/backend/internal/blog"
	"github.com/dbiderman/identityhub/backend/internal/workflows"
)

// Compile-time proof that the Reader satisfies the port the workflow declares.
//
// In a test, for the reason given in summarize/port_test.go: the assertion is
// worth keeping, the production import that used to carry it was not.
var _ workflows.BlogReader = (*blog.Reader)(nil)
