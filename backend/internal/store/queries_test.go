package store_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The isolation invariant, checked against the SQL rather than asserted about
// it.
//
// Every other guard in this application answers "is there a tenancy?" -- the
// middleware builds a Scope from the token's claims and refuses an incomplete
// one, so no query runs without both halves available. None of them answers the
// question that actually matters here: does each query *use* both halves?
//
// Nothing in SQL can. A constraint governs data at rest -- org_id and account_id
// are already NOT NULL with foreign keys, and `select * from tickets` still
// returns every tenant's rows. The only database mechanism that forces a
// predicate onto a query is Row-Level Security, which this schema deliberately
// does not use (see DECISIONS.md).
//
// So the guard is here: read the SQL, and fail the build if a statement touching
// a tenant-owned table does not filter on both halves. A missing predicate is
// the most permissive query, not the most restrictive -- it returns more rows
// rather than none -- which is why this class of mistake looks like it works.

// tenantOwned are the tables whose rows belong to one account.
//
// organizations and accounts are absent on purpose: they *are* the tenancy, so
// requiring them to filter on it would be circular.
var tenantOwned = []string{
	"users", "connections", "api_keys", "tickets",
	"idempotency_keys", "blog_posts", "audit_events",
}

// unscoped are the queries that deliberately do not filter on a tenancy, each
// with the reason it is allowed to.
//
// A list rather than a pattern, because every entry is a decision. Adding one
// should be as deliberate as writing the query, and a reviewer reading this map
// is reading the complete set of places the invariant does not hold.
var unscoped = map[string]string{
	"UserForSignIn": "signing in starts from an email address and nothing else, " +
		"so this is where a tenancy is discovered rather than supplied. The email " +
		"index is unique across the table, which is what makes that safe.",
	"FindAPIKey": "the same, for machines: the key is the claim of tenancy, and " +
		"key_id is unique table-wide.",
	"TouchUser": "by primary key, and that key came from UserForSignIn, which " +
		"had already resolved the tenancy.",
}

var (
	namedQuery = regexp.MustCompile(`(?m)^-- name: (\w+) :(\w+)\s*$`)
	insertInto = regexp.MustCompile(`insert\s+into\s+(\w+)\s*\(([^)]*)\)`)
)

func TestEveryTenantOwnedQueryFiltersOnBothHalves(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("../../db/queries/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no query files found: %v", err)
	}

	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}

		for name, sql := range queriesIn(string(body)) {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				table, found := tenantTableIn(sql)
				if !found {
					return
				}
				if reason, allowed := unscoped[name]; allowed {
					t.Logf("deliberately unscoped: %s", reason)
					return
				}

				region := scopingRegion(sql)
				for _, half := range []string{"org_id", "account_id"} {
					if !strings.Contains(region, half) {
						t.Errorf("touches %s but does not constrain %s.\n"+
							"A query that filters on one half returns every other "+
							"account's rows. If this is deliberate, add it to the "+
							"unscoped map with the reason.\n\n%s",
							table, half, strings.TrimSpace(sql))
					}
				}
			})
		}
	}
}

// queriesIn splits a file into its named queries.
func queriesIn(body string) map[string]string {
	found := map[string]string{}

	marks := namedQuery.FindAllStringSubmatchIndex(body, -1)
	for i, mark := range marks {
		end := len(body)
		if i+1 < len(marks) {
			end = marks[i+1][0]
		}
		name := body[mark[2]:mark[3]]
		found[name] = strings.ToLower(body[mark[1]:end])
	}
	return found
}

// tenantTableIn reports the first tenant-owned table a query touches.
func tenantTableIn(sql string) (string, bool) {
	for _, table := range tenantOwned {
		// Word-bounded so that "users" does not match "users_email_uniq", and
		// only where a table can appear.
		if regexp.MustCompile(`\b(from|join|into|update)\s+` + table + `\b`).MatchString(sql) {
			return table, true
		}
	}
	return "", false
}

// scopingRegion is the part of a statement that decides which rows it touches.
//
// For a read, an update or a delete that is the WHERE clause. For an insert
// there is no WHERE: what decides the tenancy is the column list, so a row
// cannot be written without naming both halves.
func scopingRegion(sql string) string {
	if match := insertInto.FindStringSubmatch(sql); match != nil {
		region := match[2]
		// An upsert's WHERE, when it has one, belongs to the region too.
		if where := strings.Index(sql, "where"); where >= 0 {
			region += " " + sql[where:]
		}
		return region
	}
	if where := strings.Index(sql, "where"); where >= 0 {
		return sql[where:]
	}
	return ""
}
