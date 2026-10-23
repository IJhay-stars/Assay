package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/use-assay/assay/internal/api"
)

// TestDocsAPIEndpointCoverage holds docs/api.md to issue #297: it must remain
// the human reference for every endpoint `assay serve` registers — the route
// list in api.Routes is the same list the OpenAPI coverage test pins, so a
// new route turns this test red until the page documents it — and it must
// name openapi.yaml as the schema source. The README docs table must link
// the page, so a reader arriving at the repository finds it.
func TestDocsAPIEndpointCoverage(t *testing.T) {
	doc := readRepoFile(t, "docs", "api.md")
	readme := readRepoFile(t, "README.md")

	// The section heading each route must have in docs/api.md. A route
	// registered by the server that is missing here fails with instructions
	// rather than being silently tolerated.
	headings := map[string]string{
		"GET /api/v1/scan":     "### `GET /api/v1/scan",
		"GET /api/v1/history":  "### `GET /api/v1/history",
		"GET /api/v1/exposure": "### `GET /api/v1/exposure",
		"GET /metrics":         "### `GET /metrics`",
		"GET /healthz":         "### `GET /healthz`",
		"GET /readyz":          "### `GET /readyz`",
		"GET /":                "### `GET /`",
	}

	for _, route := range api.Routes {
		want, ok := headings[route]
		if !ok {
			t.Errorf("api.Server registers %s, which docs/api.md has no section for; "+
				"document it in docs/api.md and add its heading here", route)
			continue
		}
		if !contains(doc, want) {
			t.Errorf("docs/api.md does not document %s: no %q section", route, want)
		}
	}
	for route := range headings {
		known := false
		for _, registered := range api.Routes {
			if registered == route {
				known = true
				break
			}
		}
		if !known {
			t.Errorf("docs/api.md documents %s, but api.Server does not register it", route)
		}
	}

	if !contains(doc, "[`openapi.yaml`](../openapi.yaml)") {
		t.Error("docs/api.md does not point at openapi.yaml as the schema source")
	}
	if !contains(readme, "[docs/api.md](") {
		t.Error("README does not link docs/api.md from its docs table")
	}
}

// readRepoFile reads a file from the repository root.
func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{getRepoRoot(t)}, parts...)...)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
