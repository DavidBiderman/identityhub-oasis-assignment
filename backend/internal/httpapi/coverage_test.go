package httpapi_test

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/dbiderman/identityhub/backend/api"
)

// Whether the suite actually exercises every endpoint it claims to describe.
//
// Response validation checks that what an endpoint answers matches the
// document -- but only for endpoints a test touches. An operation nothing
// exercises can answer any shape it likes, forever, while the document keeps
// claiming otherwise and the frontend's own copy of the types quietly disagrees
// with both.
//
// So the document is the checklist. It already lists every operation; this
// records which ones the suite reached and fails if any was missed. That turns
// "the end-to-end tests cover the main flows" into a statement with a build
// failure behind it.
var exercised struct {
	sync.Mutex
	operations map[string]bool
}

func recordExercised(operationID string) {
	exercised.Lock()
	defer exercised.Unlock()

	if exercised.operations == nil {
		exercised.operations = map[string]bool{}
	}
	exercised.operations[operationID] = true
}

// neverExercised are operations no test drives, each with the reason.
//
// Empty, and worth keeping that way. An entry here is a promise the document
// makes that nothing checks.
var neverExercised = map[string]string{}

func TestMain(m *testing.M) {
	code := m.Run()

	// Only meaningful after a full run against real infrastructure. A filtered
	// run exercises a subset by definition, and a run with no database skips
	// every end-to-end test, so neither says anything about coverage.
	filtered := flag.Lookup("test.run") != nil && flag.Lookup("test.run").Value.String() != ""
	if code != 0 || filtered || os.Getenv("TEST_DATABASE_URL") == "" {
		os.Exit(code)
	}

	if missing := uncovered(); len(missing) > 0 {
		fmt.Fprintf(os.Stderr,
			"\nthe end-to-end suite never exercised %d operation(s) the document describes:\n",
			len(missing))
		for _, id := range missing {
			fmt.Fprintf(os.Stderr, "  %s\n", id)
		}
		fmt.Fprintf(os.Stderr,
			"\nAn operation no test reaches is one whose responses are never checked "+
				"against the document. Add a test, or add it to neverExercised with the reason.\n")
		code = 1
	}
	os.Exit(code)
}

// uncovered lists the operations in the document that no test drove.
func uncovered() []string {
	document, err := openapi3.NewLoader().LoadFromData(api.Spec)
	if err != nil {
		return []string{fmt.Sprintf("could not read the document: %v", err)}
	}

	exercised.Lock()
	defer exercised.Unlock()

	var missing []string
	for _, item := range document.Paths.Map() {
		for _, op := range item.Operations() {
			if exercised.operations[op.OperationID] {
				continue
			}
			if _, allowed := neverExercised[op.OperationID]; allowed {
				continue
			}
			missing = append(missing, op.OperationID)
		}
	}
	sort.Strings(missing)
	return missing
}
