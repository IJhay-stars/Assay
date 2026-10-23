package attester

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/use-assay/assay/internal/attest"
	"github.com/use-assay/assay/internal/mechanics"
)

const registry = "CBK4FBIHMDTXCUPE4E3ZDVSFJSCY5FJETTKNIQPN4LFJIKKIBLKIXQ73"

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func fixturesDir(t *testing.T) string {
	return filepath.Join(repoRoot(t), "internal", "mechanics", "testdata")
}

func mustConfig(t *testing.T, js string) *Config {
	t.Helper()
	cfg, err := ParseConfig([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestParseConfigRejects(t *testing.T) {
	for name, tc := range map[string]struct{ js, want string }{
		"unknown field":       {`{"network":"testnet","contract_id":"` + registry + `","assets":[{"asset":"AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA"}],"contractid":"x"}`, "unknown field"},
		"empty run":           {`{"network":"testnet","contract_id":"` + registry + `","assets":[]}`, "empty run is an error"},
		"missing network":     {`{"contract_id":"` + registry + `","assets":[{"asset":"AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA"}]}`, "network"},
		"unknown network":     {`{"network":"testnett","contract_id":"` + registry + `","assets":[{"asset":"AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA"}]}`, "neither a known network"},
		"account as contract": {`{"network":"testnet","contract_id":"GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA","assets":[{"asset":"AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA"}]}`, "not a contract address"},
		"native asset":        {`{"network":"testnet","contract_id":"` + registry + `","assets":[{"asset":"native"}]}`, "cannot be scanned or attested"},
		"duplicate asset":     {`{"network":"testnet","contract_id":"` + registry + `","assets":[{"asset":"AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA"},{"asset":"AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA"}]}`, "listed twice"},
		"fixture without dir": {`{"network":"testnet","contract_id":"` + registry + `","assets":[{"asset":"AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA","fixture":"aqua-clear-verified"}]}`, "fixtures_dir is not set"},
		"dir without fixture": {`{"network":"testnet","contract_id":"` + registry + `","fixtures_dir":"x","assets":[{"asset":"AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA"}]}`, "has no fixture"},
		"bad interval":        {`{"network":"testnet","contract_id":"` + registry + `","watch_interval":"0s","assets":[{"asset":"AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA"}]}`, "positive duration"},
	} {
		_, err := ParseConfig([]byte(tc.js))
		if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want ErrConfig mentioning %q", name, err, tc.want)
		}
	}
}

func TestCommittedConfigsLoad(t *testing.T) {
	for _, rel := range []string{"attested.json", ".github/attester/fixtures.json"} {
		cfg, err := LoadConfig(filepath.Join(repoRoot(t), rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if cfg.RegistryPassphrase() != "Test SDF Network ; September 2015" {
			t.Errorf("%s: registry network = %q, want testnet", rel, cfg.RegistryPassphrase())
		}
	}
}

func fixtureConfig(t *testing.T, assets string) *Config {
	t.Helper()
	return mustConfig(t, `{"network":"testnet","contract_id":"`+registry+`","fixtures_dir":`+
		quote(fixturesDir(t))+`,"assets":[`+assets+`]}`)
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }

const (
	aquaEntry = `{"asset":"AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA","fixture":"aqua-clear-verified"}`
	usdcEntry = `{"asset":"USDC-GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN","fixture":"usdc-revocable-regulated"}`
	// An outage fixture: reputation did not complete, so the report is
	// undetermined and must be refused.
	aquaOutageEntry = `{"asset":"AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA","fixture":"synthetic-reputation-outage"}`
)

func TestDryRunWouldAttestMatchesAttestationRaw(t *testing.T) {
	cfg := fixtureConfig(t, aquaEntry+","+usdcEntry)
	results := DryRun(context.Background(), cfg, FixtureScan(cfg))
	if code := ExitCode(results); code != ExitWouldAttest {
		t.Fatalf("exit = %d, want 0; results %+v", code, results)
	}

	// The same derivation `assay attestation -raw` runs, on the same report.
	rep, err := FixtureScan(cfg)(context.Background(), cfg.ParsedAssets()[1])
	if err != nil {
		t.Fatal(err)
	}
	p, err := attest.FromReport(rep)
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.Join([]string{itoa(p.Severity), itoa(p.Flags), p.EvidenceHash}, "\t")
	want := "USDC-GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN\tCA2E53VHFZ6YSWQIEIPBXJQGT6VW3VKWWZO555XKRQXYJ63GEBJJGHY7\t" + raw
	if got := FormatResult(results[1]); got != want {
		t.Fatalf("line:\n got %q\nwant %q", got, want)
	}
}

func itoa(v uint32) string { return strconv.FormatUint(uint64(v), 10) }

func TestDryRunRefusesUndeterminedVerbatim(t *testing.T) {
	cfg := fixtureConfig(t, aquaOutageEntry+","+usdcEntry)
	results := DryRun(context.Background(), cfg, FixtureScan(cfg))
	if code := ExitCode(results); code != ExitRefused {
		t.Fatalf("exit = %d, want 1", code)
	}
	r := results[0]
	if r.Status != StatusRefused {
		t.Fatalf("status = %s, want refused", r.Status)
	}
	// The refusal is attest.FromReport's own error, unedited, and the SAC
	// address is still named.
	if !strings.HasPrefix(r.Reason, attest.ErrUndetermined.Error()) {
		t.Fatalf("reason %q does not start with the attest refusal", r.Reason)
	}
	want := "AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA\tCDJF2JQINO7WRFXB2AAHLONFDPPI4M3W2UM5THGQQ7JMJDIEJYC4CMPG\trefused\t" + r.Reason
	if got := FormatResult(r); got != want {
		t.Fatalf("line:\n got %q\nwant %q", got, want)
	}
}

func TestDryRunScanFailureIsOperational(t *testing.T) {
	cfg := mustConfig(t, `{"network":"testnet","contract_id":"`+registry+`","assets":[{"asset":"AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA"},{"asset":"USDC-GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"}]}`)
	fixtures := fixtureConfig(t, aquaOutageEntry)
	scanFn := func(ctx context.Context, a mechanics.Asset) (*mechanics.Report, error) {
		if a.Code == "USDC" {
			return nil, errors.New("horizon: get /assets: dial tcp: connection refused")
		}
		return FixtureScan(fixtures)(ctx, a)
	}
	results := DryRun(context.Background(), cfg, scanFn)
	// A refusal and an operational failure in one pass: the failure wins.
	if code := ExitCode(results); code != ExitOperational {
		t.Fatalf("exit = %d, want 2", code)
	}
	if results[1].Status != StatusError || !strings.Contains(results[1].Reason, "connection refused") {
		t.Fatalf("USDC result = %+v, want an error carrying the scan failure", results[1])
	}
}

func TestFixtureScanRejectsMismatchedSubject(t *testing.T) {
	cfg := fixtureConfig(t, `{"asset":"USDC-GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN","fixture":"aqua-clear-verified"}`)
	results := DryRun(context.Background(), cfg, FixtureScan(cfg))
	if results[0].Status != StatusError || !strings.Contains(results[0].Reason, "is for AQUA-") {
		t.Fatalf("result = %+v, want an error naming the fixture's real asset", results[0])
	}
}

func TestFormatResultKeepsOneLinePerAsset(t *testing.T) {
	r := Result{
		Asset:  mechanics.Asset{Code: "AQUA", Issuer: "GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA"},
		Status: StatusError,
		Reason: "line one\nline\ttwo\\",
	}
	got := FormatResult(r)
	if strings.Count(got, "\n") != 0 || strings.Count(got, "\t") != 3 {
		t.Fatalf("FormatResult(%q) = %q, want one line with four fields", r.Reason, got)
	}
	if !strings.HasSuffix(got, `line one\nline\ttwo\\`) {
		t.Fatalf("reason not escaped verbatim: %q", got)
	}
}

func TestExitCodeOfEmptyPassIsOperational(t *testing.T) {
	if ExitCode(nil) != ExitOperational {
		t.Fatal("an empty pass must not exit 0")
	}
}

func TestDiff(t *testing.T) {
	base, err := ParseOutput(strings.Join([]string{
		"AQUA-G1\tC1\t0\t0\taaa",
		"USDC-G2\tC2\t2\t18\tbbb",
		"VELO-G3\tC3\trefused\tattest: scan is undetermined",
	}, "\n") + "\n")
	if err != nil {
		t.Fatal(err)
	}
	head, err := ParseOutput(strings.Join([]string{
		"AQUA-G1\tC1\t0\t0\taaa",
		"USDC-G2\tC2\t3\t22\tccc",
		"XRP-G4\tC4\t0\t0\tddd",
	}, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := Diff(base, head)
	want := []FieldChange{
		{"USDC-G2", "severity", "2", "3"},
		{"USDC-G2", "flags", "18", "22"},
		{"USDC-G2", "evidence_hash", "bbb", "ccc"},
		{"VELO-G3", "asset", "refused", "(absent)"},
		{"XRP-G4", "asset", "(absent)", "attest severity=0 flags=0"},
	}
	if len(got) != len(want) {
		t.Fatalf("changes = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("change %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	md := RenderMarkdown(got, head, "")
	for _, s := range []string{"5 field(s) changed", "| `USDC-G2` | severity | `2` | `3` |"} {
		if !strings.Contains(md, s) {
			t.Errorf("markdown missing %q:\n%s", s, md)
		}
	}
	if md := RenderMarkdown(nil, head, ""); !strings.Contains(md, "No change") {
		t.Errorf("unchanged diff should say so:\n%s", md)
	}
	if md := RenderMarkdown(nil, head, "base has no attester"); !strings.Contains(md, "No comparison:** base has no attester") {
		t.Errorf("base note missing:\n%s", md)
	}
}

func TestParseOutputRejectsMalformedLines(t *testing.T) {
	for _, bad := range []string{
		"AQUA-G1\tC1\t0\t0",                          // truncated
		"AQUA-G1\tC1\tmaybe\treason",                 // unknown status
		"AQUA-G1\tC1\t0\t0\ta\nAQUA-G1\tC1\t0\t0\tb", // duplicate
	} {
		if _, err := ParseOutput(bad); err == nil {
			t.Errorf("ParseOutput(%q) accepted malformed output", bad)
		}
	}
}
