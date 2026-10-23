package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/use-assay/assay/internal/api"
	"github.com/use-assay/assay/internal/stellarexpert"
)

// Issuer-controlled strings reach the logs: an issuer's home_domain and its
// directory name are fetched from sources the issuer can influence, and the
// asset record a source echoes is quoted in the error a mismatch produces.
// Logs are an audit surface (#147), so a string that smuggles in a newline
// must not be able to forge a second record — in particular a fake "scan
// outcome" line saying the scan was complete. These tests feed a hostile
// fixture through a real scan and count the records that come out (#445).

// forgedRecord is what an attacker would want a log reader to see: a complete
// scan, in both of the formats the server can log in.
const forgedRecord = "\n{\"time\":\"2026-10-22T00:00:00Z\",\"level\":\"INFO\",\"msg\":\"scan outcome\",\"outcome\":\"complete\",\"status\":200}" +
	"\r\ntime=2026-10-22T00:00:00Z level=INFO msg=\"scan outcome\" outcome=complete status=200\n"

// hostile is an issuer string carrying newlines, quotes, a backslash and two
// forged records.
const hostile = `evil.example" injected="1 \ ` + forgedRecord + `{"outcome":"complete"}`

// injectionCase is one route an issuer string takes into the logs.
type injectionCase struct {
	name string
	// horizon serves the Horizon endpoints; it is where the hostile string
	// is placed for that route.
	horizon http.HandlerFunc
	// directoryName is placed in StellarExpert's directory entry.
	directoryName string
	// wantClass is the outcome the scan must be logged as, whatever the
	// hostile string claims.
	wantClass string
}

func usdcAssetRecord(code string) string {
	b, _ := json.Marshal(code)
	return `{"_embedded":{"records":[{"asset_type":"credit_alphanum4","asset_code":` + string(b) +
		`,"asset_issuer":"GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN","flags":{"auth_required":false,"auth_revocable":false,"auth_immutable":true,"auth_clawback_enabled":false},"accounts":{"authorized":1,"unauthorized":0}}]}}`
}

func issuerAccount(homeDomain string) string {
	b, _ := json.Marshal(homeDomain)
	return `{"account_id":"GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN","home_domain":` + string(b) +
		`,"flags":{"auth_required":false,"auth_revocable":false,"auth_immutable":true,"auth_clawback_enabled":false}}`
}

func horizonServing(assetCode, homeDomain string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/assets"):
			_, _ = w.Write([]byte(usdcAssetRecord(assetCode)))
		case strings.HasPrefix(r.URL.Path, "/accounts/"):
			_, _ = w.Write([]byte(issuerAccount(homeDomain)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func injectionCases() []injectionCase {
	return []injectionCase{
		{
			// The clean baseline every hostile case is compared against.
			name:          "clean",
			horizon:       horizonServing("USDC", "example.com"),
			directoryName: "Circle",
			wantClass:     "complete",
		},
		{
			// The issuer string reaches the error field, and the handler's
			// "scan failed" line, verbatim: Horizon echoes a different code,
			// the scan refuses it, and the error quotes what was echoed.
			name:          "echoed asset code",
			horizon:       horizonServing(hostile, "example.com"),
			directoryName: "Circle",
			wantClass:     "upstream_failure",
		},
		{
			name:          "home_domain",
			horizon:       horizonServing("USDC", hostile),
			directoryName: "Circle",
		},
		{
			name:          "directory name",
			horizon:       horizonServing("USDC", "example.com"),
			directoryName: hostile,
			wantClass:     "complete",
		},
	}
}

// scanWithHandler runs one scan of logTestAsset against the case's stubs and
// returns everything the server logged, as written.
func scanWithHandler(t *testing.T, tc injectionCase, newHandler func(io.Writer) slog.Handler) string {
	t.Helper()
	var buf strings.Builder
	srv := api.NewServer(slog.New(newHandler(&buf)))

	h := httptest.NewServer(tc.horizon)
	t.Cleanup(h.Close)
	srv.Scanner.Horizon.BaseURL = h.URL

	entry, _ := json.Marshal(map[string]any{
		"address": "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN",
		"name":    tc.directoryName,
		"domain":  "centre.io",
		"tags":    []string{"issuer"},
	})
	e := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/directory/blocked-domains/") {
			_, _ = w.Write([]byte(`{"domain":"example.com","blocked":false}`))
			return
		}
		_, _ = w.Write(entry)
	}))
	t.Cleanup(e.Close)
	srv.Scanner.Expert = stellarexpert.NewWithOptions(e.URL, stellarexpert.Options{})

	srv.Scanner.Toml.HTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("[[CURRENCIES]]\n")),
			Request:    req,
		}, nil
	})}

	doScan(srv.Handler(), logTestAsset, nil)
	return buf.String()
}

func lines(out string) []string {
	return strings.Split(strings.TrimSuffix(out, "\n"), "\n")
}

// TestIssuerStringsCannotForgeJSONLogRecords: with the JSON handler, every
// line is exactly one well-formed record, a hostile string produces no more
// records than the clean scan, exactly one of them is the scan outcome, and
// the hostile string survives intact inside a field rather than being split
// across lines.
func TestIssuerStringsCannotForgeJSONLogRecords(t *testing.T) {
	newJSON := func(w io.Writer) slog.Handler { return slog.NewJSONHandler(w, nil) }
	clean := len(lines(scanWithHandler(t, injectionCases()[0], newJSON)))

	for _, tc := range injectionCases() {
		out := scanWithHandler(t, tc, newJSON)
		ls := lines(out)

		var outcomes []map[string]any
		for _, l := range ls {
			var rec map[string]any
			if err := json.Unmarshal([]byte(l), &rec); err != nil {
				t.Fatalf("%s: log line is not one JSON record: %q", tc.name, l)
			}
			if rec["msg"] == "scan outcome" {
				outcomes = append(outcomes, rec)
			}
		}
		if len(outcomes) != 1 {
			t.Fatalf("%s: %d scan outcome records, want exactly 1:\n%s", tc.name, len(outcomes), out)
		}
		if tc.wantClass != "" && outcomes[0]["outcome"] != tc.wantClass {
			t.Errorf("%s: outcome = %v, want %s", tc.name, outcomes[0]["outcome"], tc.wantClass)
		}
		if tc.name != "clean" && tc.wantClass == "upstream_failure" {
			// One extra record is the handler's own "scan failed" line, which
			// a failing scan writes by design; the hostile string adds none.
			if len(ls) != clean+1 {
				t.Errorf("%s: %d records, want %d (clean scan plus the scan failed line)", tc.name, len(ls), clean+1)
			}
			errField, _ := outcomes[0]["error"].(string)
			if !strings.Contains(errField, hostile) {
				t.Errorf("%s: hostile string not carried intact in the error field: %q", tc.name, errField)
			}
		} else if len(ls) != clean {
			t.Errorf("%s: %d records, want %d like the clean scan:\n%s", tc.name, len(ls), clean, out)
		}
	}
}

// TestIssuerStringsCannotForgeTextLogRecords: the same property for the text
// handler, which is what `assay serve` logs with. Text records are one per
// line with quoted values, so the check is the line count and that exactly
// one line starts a scan outcome record.
func TestIssuerStringsCannotForgeTextLogRecords(t *testing.T) {
	newText := func(w io.Writer) slog.Handler { return slog.NewTextHandler(w, nil) }
	clean := len(lines(scanWithHandler(t, injectionCases()[0], newText)))

	for _, tc := range injectionCases() {
		out := scanWithHandler(t, tc, newText)
		ls := lines(out)

		want := clean
		if tc.wantClass == "upstream_failure" {
			want = clean + 1
		}
		if len(ls) != want {
			t.Errorf("%s: %d log lines, want %d:\n%s", tc.name, len(ls), want, out)
		}
		var outcomes int
		for _, l := range ls {
			if !strings.HasPrefix(l, "time=") {
				t.Errorf("%s: line does not start a record: %q", tc.name, l)
			}
			if strings.Contains(l, ` msg="scan outcome" `) {
				outcomes++
			}
		}
		if outcomes != 1 {
			t.Errorf("%s: %d scan outcome lines, want exactly 1:\n%s", tc.name, outcomes, out)
		}
	}
}
