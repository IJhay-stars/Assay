package api_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/use-assay/assay/internal/api"
	"github.com/use-assay/assay/internal/history"
	"github.com/use-assay/assay/internal/scan"
	"github.com/use-assay/assay/internal/stellarexpert"
)

// updateGolden regenerates the committed golden scan response. It is OFF by
// default on purpose: docs/api-versioning.md promises that removing or
// renaming a field is a breaking change, and a golden file that rewrites
// itself on every run would enforce nothing. The only way an addition reaches
// the golden file is by an explicit `go test ./internal/api/ -run Golden
// -update` followed by a review of the diff — see the update procedure in
// docs/api-versioning.md.
var updateGolden = flag.Bool("update", false, "rewrite the golden scan-response file from this build's handler output")

const (
	// goldenSubject is the fixture directory under internal/mechanics/testdata
	// (provenance: internal/mechanics/testdata/PROVENANCE.md). It is the
	// subject docs/eval.md uses for the "regulated stablecoin, legitimate
	// auth_revocable, no stellar.toml" case, so this golden pins the response
	// shape for a report that carries medium severity, an unverified domain,
	// and a 404'd stellar.toml — a realistic non-trivial render.
	goldenSubject = "usdc-revocable-regulated"
	goldenIssuer  = "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"
	goldenAsset   = "USDC-" + goldenIssuer
)

// goldenClock is the pinned scan instant, shared with eval's fixtureTime
// (internal/eval/load.go) so every fixture-derived render in the repo claims
// the same capture instant. Every timestamp in the golden response derives
// from this value and nothing else, which is what makes the file stable.
var goldenClock = time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)

// TestScanResponseGolden pins the full JSON shape of a scan response: field
// names, types, and nesting, exactly as GET /api/v1/scan renders them.
//
// docs/api-versioning.md promises that removing or renaming a field is a
// breaking change requiring a version bump. Nothing else enforces that
// promise: the handler tests assert individual fields, so a field that
// disappears (or is renamed to something no test names) passes them all.
// This test compares the whole rendered document against a committed golden
// file, byte for byte after key-sort normalization — a removal fails, a
// rename fails (it is a removal plus an addition), and a type change fails.
//
// Additions also fail the byte comparison, and that is deliberate: the update
// procedure in docs/api-versioning.md is the only path a new field takes into
// the golden file, so every addition is an explicit, reviewed decision.
//
// The render is the production path end to end: the real http.ServeMux, the
// real handleScan, a real scan.Scanner running the real engine. Only the
// upstreams are stubbed, and they replay the committed fixture captures of a
// real pubnet asset (internal/mechanics/testdata/usdc-revocable-regulated).
// The scanner's clock (scan.Scanner.Now) and the StellarExpert client's clock
// (stellarexpert.Options.Now) are pinned to goldenClock, so every timestamp
// the response carries is fixed and the golden file cannot drift.
func TestScanResponseGolden(t *testing.T) {
	h := goldenScanHandler(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/scan?asset="+goldenAsset, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", rec.Code, rec.Body.String())
	}

	got := normalizeGoldenJSON(t, rec.Body.Bytes())
	goldenPath := filepath.Join("testdata", "scan-"+goldenSubject+".json")

	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("create testdata dir: %v", err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden file: %v", err)
		}
		t.Logf("wrote %s (%d bytes); review the diff before committing — see docs/api-versioning.md", goldenPath, len(got))
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden file %s: %v\nIf this is the first run, generate it with:\n  go test ./internal/api/ -run Golden -update", goldenPath, err)
	}

	if bytes.Equal(got, want) {
		return
	}

	removed, added, changed := diffJSONShapes(want, got)
	var b strings.Builder
	fmt.Fprintf(&b, "the scan response no longer matches the committed golden shape %s\n", goldenPath)
	fmt.Fprintf(&b, "removed paths (%d):\n", len(removed))
	for _, p := range removed {
		fmt.Fprintf(&b, "  - %s\n", p)
	}
	fmt.Fprintf(&b, "added paths (%d):\n", len(added))
	for _, p := range added {
		fmt.Fprintf(&b, "  + %s\n", p)
	}
	fmt.Fprintf(&b, "changed paths (%d):\n", len(changed))
	for _, p := range changed {
		fmt.Fprintf(&b, "  ~ %s\n", p)
	}
	if len(removed) == 0 && len(added) == 0 && len(changed) == 0 {
		b.WriteString("(no path-level difference; only formatting or key order moved — the renderer must stay deterministic)\n")
	}
	b.WriteString("Removing or renaming a field is a breaking change: bump the version per docs/api-versioning.md.\n")
	b.WriteString("If the change is intended (additive or version-bumped), regenerate with:\n")
	b.WriteString("  go test ./internal/api/ -run Golden -update\n")
	b.WriteString("and commit the golden diff together with the version bump.\n")
	t.Fatal(b.String())
}

// TestGoldenShapeDiffNamesARemovedField pins the comparator the golden test
// relies on: a removed field is reported as removed, and a renamed field is
// reported as a removal plus an addition. The golden test fails on the bytes
// alone; this test makes sure the diagnostic it prints actually names the
// breakage, so a failing CI run tells a maintainer which field disappeared
// rather than only that something did.
func TestGoldenShapeDiffNamesARemovedField(t *testing.T) {
	want := []byte(`{
		"schema_version": 1,
		"asset": {"code": "USDC", "issuer": "GA5Z…"},
		"severity": "medium",
		"evidence": [{"source": "horizon", "retrieved_at": "2026-08-10T00:00:00Z"}]
	}`)
	// The issuer field is gone and severity was renamed to sevrity.
	got := []byte(`{
		"schema_version": 1,
		"asset": {"code": "USDC"},
		"sevrity": "medium",
		"evidence": [{"source": "horizon", "retrieved_at": "2026-08-10T00:00:00Z"}]
	}`)

	removed, added, changed := diffJSONShapes(want, got)

	for _, p := range []string{"asset.issuer", "severity"} {
		if !contains(removed, p) {
			t.Errorf("removed = %v, want it to name %q", removed, p)
		}
	}
	if !contains(added, "sevrity") {
		t.Errorf("added = %v, want it to name the renamed field's new home %q", added, "sevrity")
	}
	if len(changed) != 0 {
		t.Errorf("changed = %v, want empty: the rest of the document is identical", changed)
	}
}

// goldenScanHandler builds the handler under test: the production Server over
// a real scan.Scanner whose upstreams replay the committed fixture captures.
func goldenScanHandler(t *testing.T) http.Handler {
	t.Helper()

	fixtures := filepath.Join("..", "mechanics", "testdata", goldenSubject)
	assetRaw := readGoldenFixture(t, filepath.Join(fixtures, "asset.json"))
	accountRaw := readGoldenFixture(t, filepath.Join(fixtures, "account.json"))
	directoryRaw := readGoldenFixture(t, filepath.Join(fixtures, "directory.json"))
	blockedRaw := readGoldenFixture(t, filepath.Join(fixtures, "blocked.json"))

	// The stellar.toml fixture records an HTTP status marker rather than a
	// document — the real circle.com answer when this capture was taken — so
	// the toml stub replays exactly that status.
	var account struct {
		HomeDomain string `json:"home_domain"`
	}
	if err := json.Unmarshal(accountRaw, &account); err != nil {
		t.Fatalf("decode %s account.json: %v", goldenSubject, err)
	}
	if account.HomeDomain == "" {
		t.Fatalf("%s account.json advertises no home_domain; the fixture set changed under this golden", goldenSubject)
	}
	tomlStatus, err := strconv.Atoi(strings.TrimSpace(string(readGoldenFixture(t, filepath.Join(fixtures, "stellar.toml.status")))))
	if err != nil {
		t.Fatalf("parse %s stellar.toml.status: %v", goldenSubject, err)
	}

	horizonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/assets":
			// The fixture is the single record Horizon embeds; the client
			// reads it from the _embedded envelope, so the stub wraps it the
			// same way the real API does.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"_embedded": map[string]any{"records": []json.RawMessage{assetRaw}},
			})
		case "/accounts/" + goldenIssuer:
			_, _ = w.Write(accountRaw)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(horizonSrv.Close)

	expertSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/explorer/directory/" + goldenIssuer:
			_, _ = w.Write(directoryRaw)
		case "/explorer/directory/blocked-domains/" + account.HomeDomain:
			_, _ = w.Write(blockedRaw)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(expertSrv.Close)

	tomlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(tomlStatus)
	}))
	t.Cleanup(tomlSrv.Close)

	now := func() time.Time { return goldenClock }

	sc := scan.New()
	sc.Now = now
	sc.Horizon.BaseURL = horizonSrv.URL
	// The StellarExpert client stamps its own answers with its own clock
	// (Answer.FetchedAt), which lands in Evidence.RetrievedAt — so the
	// golden render pins the client's clock too, not only the scanner's.
	expertOpts := stellarexpert.DefaultOptions()
	expertOpts.Now = now
	sc.Expert = stellarexpert.NewWithOptions(stellarexpert.DefaultURL, expertOpts)
	// The BaseURL stays the real api.stellar.expert: it is what the subject's
	// DirectoryURL and BlockedURL — and therefore the evidence attribution in
	// the golden — are built from, and the captures came from that source. Only
	// the transport is redirected to the stub. Pointing BaseURL at the
	// httptest URL instead would bake its random port into the golden file and
	// make the render nondeterministic.
	sc.Expert.HTTP.Transport = &goldenSingleHostTransport{host: strings.TrimPrefix(expertSrv.URL, "http://")}
	// The sep1 fetcher targets the issuer's home_domain URL; route it to the
	// stub without rewriting the URL the evidence attributes the claim to.
	sc.Toml.HTTP.Transport = &goldenSingleHostTransport{host: strings.TrimPrefix(tomlSrv.URL, "http://")}

	srv := &api.Server{
		Scanner: sc,
		History: history.New(),
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return srv.Handler()
}

// goldenSingleHostTransport routes every request to one stub host, so the sep1
// Fetcher — whose target URL is derived from the issuer's home_domain — can be
// pointed at an httptest server without changing sep1.URLFor.
type goldenSingleHostTransport struct{ host string }

func (tr *goldenSingleHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	u := *req.URL
	u.Scheme = "http"
	u.Host = tr.host
	req.URL = &u
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("round trip: %w", err)
	}
	return resp, nil
}

func readGoldenFixture(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return b
}

// normalizeGoldenJSON renders a response body canonically: parsed and
// re-marshaled so every object's keys come out sorted (Go marshals
// map[string]any lexicographically) and the indentation matches the handler's.
// The golden file then pins the shape and the values — never struct field
// order or whitespace — while still being byte-comparable.
func normalizeGoldenJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("response is not JSON: %v\nbody:\n%s", err, raw)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("re-encode response: %v", err)
	}
	return append(out, '\n')
}

// diffJSONShapes compares two rendered documents at the level this golden
// enforces: the set of JSON paths and the value at each. It returns the paths
// the response no longer has (removals), the paths it gained (additions), and
// the paths whose value changed. A rename surfaces as a removal plus an
// addition — exactly the pair of facts a consumer of the old shape experiences.
func diffJSONShapes(want, got []byte) (removed, added, changed []string) {
	var w, g any
	if err := json.Unmarshal(want, &w); err != nil {
		w = string(want)
	}
	if err := json.Unmarshal(got, &g); err != nil {
		g = string(got)
	}
	ws := jsonShape(w, "", map[string]string{})
	gs := jsonShape(g, "", map[string]string{})

	for p, wv := range ws {
		gv, ok := gs[p]
		switch {
		case !ok:
			removed = append(removed, p)
		case gv != wv:
			changed = append(changed, fmt.Sprintf("%s (%s -> %s)", p, wv, gv))
		}
	}
	for p := range gs {
		if _, ok := ws[p]; !ok {
			added = append(added, p)
		}
	}
	sort.Strings(removed)
	sort.Strings(added)
	sort.Strings(changed)
	return removed, added, changed
}

// jsonShape flattens a decoded JSON document into path → rendered-value pairs.
// Objects contribute a path per key, arrays contribute a length marker plus a
// path per element (indices included, so an element that disappears is named),
// and scalars contribute their canonical JSON rendering — so a type change
// (say, a string becoming a number) shows up as a changed path.
func jsonShape(v any, path string, out map[string]string) map[string]string {
	switch tv := v.(type) {
	case map[string]any:
		out[path] = "object"
		keys := make([]string, 0, len(tv))
		for k := range tv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := k
			if path != "" {
				p = path + "." + k
			}
			jsonShape(tv[k], p, out)
		}
	case []any:
		out[path] = fmt.Sprintf("array(%d)", len(tv))
		for i, el := range tv {
			jsonShape(el, fmt.Sprintf("%s[%d]", path, i), out)
		}
	default:
		b, err := json.Marshal(tv)
		if err != nil {
			b = []byte(fmt.Sprintf("%v", tv))
		}
		out[path] = string(b)
	}
	return out
}
