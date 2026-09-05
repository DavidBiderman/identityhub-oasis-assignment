// Package docscheck tests the documents against the code they describe.
//
// The design documents are graded artifacts, and they have drifted from the
// code three times: a package that was renamed, a helper that was deleted, a
// column that never existed. Each was found by a human reading carefully, which
// is the most expensive way to find it and the least reliable.
//
// This catches the mechanical half. A document that names an identifier, a file
// or a column now has to name one that exists. It cannot check whether a
// paragraph is *true* -- "the digest files through the public API" names nothing
// that could be missing -- so the honest claim is that this closes the class of
// drift that is checkable, not all of it.
package docscheck_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot is two levels above this package.
const repoRoot = "../../.."

// documents are the files a reviewer is asked to read.
//
// A document listed here that does not exist is skipped rather than failed:
// which documents ship is a decision, and this test's job is to check what is
// in them, not to insist on which ones there are. The skip is reported so that
// a typo in this list is visible rather than silent.
var documents = []string{
	"README.md",
	"docs/DECISIONS.md",
	"docs/FLOWS.md",
	"docs/CONNECTING-JIRA.md",
	"docs/AUTHENTICATION.md",
}

// backticked spans are the only thing examined. Prose is not checked, and could
// not be.
var (
	backticked = regexp.MustCompile("`([^`\n]+)`")

	// pkg.Symbol, where pkg is one of ours.
	qualified = regexp.MustCompile(`^([a-z][a-z0-9]*)\.([A-Z][A-Za-z0-9_]*)$`)

	// A path into the tree. Anything with a slash under a directory we own.
	pathLike = regexp.MustCompile(`^(internal|cmd|db|api|frontend/src)/[\w./-]+$`)

	// A column or parameter name. Narrow on purpose: identifiers ending in _id
	// are the ones that name a tenancy, which is the claim most worth checking.
	columnLike = regexp.MustCompile(`^[a-z]+(_[a-z]+)*_id$`)

	// An unqualified Go identifier: camelCase with an internal capital, so a
	// prose word cannot match. This is the shape of a helper -- `inScope`,
	// `parseAction` -- and a deleted helper is the drift that has actually
	// happened.
	bareIdentifier = regexp.MustCompile(`^[a-z][a-z0-9]*[A-Z][A-Za-z0-9]*$`)
)

// notOurs are identifiers with this shape that belong to somebody else's
// vocabulary. Listed rather than pattern-matched, because the list is short and
// a pattern loose enough to cover it would let a deleted helper through.
var notOurs = map[string]bool{
	"sessionStorage": true, // browser
	"localStorage":   true, // browser
	"apiKey":         true, // an OpenAPI security scheme name
	"accessToken":    true, // the same
	"minLength":      true, // an OpenAPI keyword
	"maxLength":      true, // the same
	"maxItems":       true, // the same
	"datePublished":  true, // schema.org, in the blog feed
}

func TestTheDocumentsNameThingsThatExist(t *testing.T) {
	t.Parallel()

	packages := ourPackages(t)
	schema := read(t, filepath.Join(repoRoot, "backend/db/migrations/00001_schema.sql"))

	for _, document := range documents {
		t.Run(document, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(repoRoot, document)
			if _, err := os.Stat(path); err != nil {
				t.Skipf("not in this tree; nothing to check")
			}

			body := read(t, path)
			for _, line := range strings.Split(body, "\n") {
				for _, match := range backticked.FindAllStringSubmatch(line, -1) {
					check(t, match[1], packages, schema)
				}
			}
		})
	}
}

func check(t *testing.T, span string, packages map[string]string, schema string) {
	t.Helper()

	switch {
	case qualified.MatchString(span):
		parts := qualified.FindStringSubmatch(span)
		dir, ours := packages[parts[1]]
		if !ours {
			return // some other library's symbol, or prose that looks like one
		}
		if !declaredIn(t, dir, parts[2]) {
			t.Errorf("%q: package %s declares no %s", span, parts[1], parts[2])
		}

	case pathLike.MatchString(span):
		// Documents refer to backend paths without the backend/ prefix.
		_, err := os.Stat(filepath.Join(repoRoot, "backend", span))
		if err != nil {
			if _, err = os.Stat(filepath.Join(repoRoot, span)); err != nil {
				t.Errorf("%q: no such file or directory", span)
			}
		}

	case columnLike.MatchString(span):
		if !strings.Contains(schema, span) {
			t.Errorf("%q: no such column in the schema", span)
		}

	case bareIdentifier.MatchString(span) && !notOurs[span]:
		for _, dir := range packages {
			if declaredIn(t, dir, span) {
				return
			}
		}
		t.Errorf("%q: no package declares it", span)
	}
}

// ourPackages maps a package's short name to the directory holding it.
func ourPackages(t *testing.T) map[string]string {
	t.Helper()

	found := map[string]string{}
	for _, root := range []string{"backend/internal", "backend/cmd", "backend/api"} {
		walk(t, filepath.Join(repoRoot, root), func(dir string) {
			if hasGo(t, dir) {
				found[filepath.Base(dir)] = dir
			}
		})
	}
	return found
}

// declaredIn reports whether a package declares an identifier.
//
// A declaration rather than a mention. A substring search would pass on a name
// that survives only in a comment -- which is exactly how `summarize.Summarizer`
// stayed in the document after the interface moved to its consumer.
func declaredIn(t *testing.T, dir, symbol string) bool {
	t.Helper()

	quoted := regexp.QuoteMeta(symbol)
	declaration := regexp.MustCompile(
		`(?m)^(` +
			`(func|type|var|const)\s+(\([^)]*\)\s*)?` + // a top-level declaration
			`|\t)` + // a const/var block entry, or a struct field
			quoted + `\b` +
			// or a name this code chose in a string: a JSON tag, a wire field, a
			// workflow id. Those are declarations too, just not of Go symbols.
			`|"` + quoted + `[",:]`)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		if declaration.MatchString(read(t, filepath.Join(dir, entry.Name()))) {
			return true
		}
	}
	return false
}

func hasGo(t *testing.T, dir string) bool {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".go") {
			return true
		}
	}
	return false
}

func walk(t *testing.T, root string, visit func(string)) {
	t.Helper()

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			visit(path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}
