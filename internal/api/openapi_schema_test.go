package api

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/use-assay/assay/internal/mechanics"
	"github.com/use-assay/assay/internal/temporal"
)

// TestOpenAPISchemasMatchHandlerEmittedFields holds openapi.yaml's component
// schemas to the Go types the handlers actually marshal. Every JSON field a
// type can emit must be documented, and every documented property must exist
// on the type — a schema that names a field the handler never emits is drift
// of the same kind as a missing route, and a generated client built from it
// would be wrong in both directions.
//
// The comparison is field-name level: types and nesting are checked by the
// document itself being reviewed against the handler, but the names are what
// a consumer's generated code binds to, so they are pinned here.
func TestOpenAPISchemasMatchHandlerEmittedFields(t *testing.T) {
	doc := readOpenAPIDoc(t)

	for _, tc := range []struct {
		schema string
		rt     reflect.Type
	}{
		// The scan response is the report itself; the API returns it
		// unwrapped, so Report is the contract.
		{"Report", reflect.TypeOf(mechanics.Report{})},
		{"Asset", reflect.TypeOf(mechanics.Asset{})},
		{"Evidence", reflect.TypeOf(mechanics.Evidence{})},
		{"Finding", reflect.TypeOf(mechanics.Finding{})},
		// The history response and its observation/transition entries.
		{"Observation", reflect.TypeOf(temporal.Observation{})},
		{"Transition", reflect.TypeOf(temporal.Transition{})},
		{"History", reflect.TypeOf(historyResponse{})},
		// The exposure response and its per-asset entries.
		{"Exposure", reflect.TypeOf(exposureResponse{})},
		{"ExposureAsset", reflect.TypeOf(exposureAsset{})},
		// The readiness response, per upstream.
		{"Readiness", reflect.TypeOf(Readiness{})},
		{"UpstreamStatus", reflect.TypeOf(UpstreamStatus{})},
		// The metrics counters.
		{"Metrics", reflect.TypeOf(metricsBody{})},
	} {
		t.Run(tc.schema, func(t *testing.T) {
			documented := schemaProperties(t, doc, tc.schema)
			if len(documented) == 0 {
				t.Fatalf("schema %s has no documented properties", tc.schema)
			}
			emitted := jsonFieldNames(tc.rt)

			for name := range emitted {
				if !documented[name] {
					t.Errorf("field %q is emitted by %s but not documented in the %s schema",
						name, tc.rt.Name(), tc.schema)
				}
			}
			for name := range documented {
				if !emitted[name] {
					t.Errorf("property %q is documented in the %s schema but %s never emits it",
						name, tc.schema, tc.rt.Name())
				}
			}
		})
	}
}

// readOpenAPIDoc returns openapi.yaml from the repository root, located
// relative to this test file so it works regardless of the directory go test
// runs in. It duplicates api_test's readOpenAPI because the two helpers live
// in different packages.
func readOpenAPIDoc(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "openapi.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// schemaProperties extracts the top-level property names of one named schema
// under components.schemas. The document's indentation is fixed (schema at 4
// spaces, properties at 6, keys at 8), so a line scan is enough; nested keys
// are deeper and are deliberately not collected.
func schemaProperties(t *testing.T, doc, schema string) map[string]bool {
	t.Helper()

	props := map[string]bool{}
	inSchema := false
	inProperties := false
	header := "    " + schema + ":"

	scanner := bufio.NewScanner(strings.NewReader(doc))
	for scanner.Scan() {
		line := scanner.Text()

		if !inSchema {
			if line == header {
				inSchema = true
			}
			continue
		}
		// A key at or below the schema's own indentation ends the block.
		if strings.TrimSpace(line) != "" && len(line)-len(strings.TrimLeft(line, " ")) <= 4 {
			break
		}
		switch {
		case line == "      properties:":
			inProperties = true
		case inProperties && len(line)-len(strings.TrimLeft(line, " ")) > 8:
			// Nested keys and required-list items live deeper; skip them.
		case inProperties:
			name, ok := cutYAMLKey(line, 8)
			if ok {
				props[name] = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan openapi.yaml: %v", err)
	}
	return props
}

// cutYAMLKey returns the key of a "        name:" line indented exactly
// indent spaces. A line with any other indentation, or any content after the
// colon, is not a plain property key and yields ok=false.
func cutYAMLKey(line string, indent int) (string, bool) {
	if len(line)-len(strings.TrimLeft(line, " ")) != indent {
		return "", false
	}
	key, rest, found := strings.Cut(strings.TrimLeft(line, " "), ":")
	if !found || strings.TrimSpace(rest) != "" || key == "" || strings.ContainsAny(key, " #-") {
		return "", false
	}
	return key, true
}

// jsonFieldNames returns the JSON object keys a struct type can emit,
// including keys behind omitempty, which a marshal of the zero value would
// omit and therefore could not be used to pin the schema against.
func jsonFieldNames(rt reflect.Type) map[string]bool {
	names := map[string]bool{}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		names[name] = true
	}
	return names
}
