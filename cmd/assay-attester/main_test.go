package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/use-assay/assay/internal/attester"
	"github.com/use-assay/assay/internal/mechanics"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func testDeps(scan attester.ScanFunc) (deps, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return deps{
		stdout:   &out,
		stderr:   &errOut,
		liveScan: scan,
		horizon:  func(string) attester.HorizonReader { panic("no Horizon in this test") },
	}, &out, &errOut
}

func noNetwork(context.Context, mechanics.Asset) (*mechanics.Report, error) {
	panic("live scan reached in a test")
}

func TestRefusesModesThatAreNotImplemented(t *testing.T) {
	cfg := filepath.Join(repoRoot(t), "attested.json")
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no config":              {[]string{"--dry-run", "--once"}, "--config is required"},
		"signing (no --dry-run)": {[]string{"--config", cfg, "--once"}, "signing is out of scope"},
		"scheduled dry-run":      {[]string{"--config", cfg, "--dry-run"}, "pass --once"},
		"watch with dry-run":     {[]string{"--config", cfg, "--watch", "--dry-run"}, "cannot be combined"},
		"positional arguments":   {[]string{"--config", cfg, "--dry-run", "--once", "extra"}, "unexpected arguments"},
		"missing config file":    {[]string{"--config", "does-not-exist.json", "--dry-run", "--once"}, "invalid config"},
	} {
		d, out, errOut := testDeps(noNetwork)
		if code := run(context.Background(), tc.args, d); code != attester.ExitOperational {
			t.Errorf("%s: exit = %d, want 2", name, code)
		}
		if out.Len() != 0 {
			t.Errorf("%s: wrote to stdout: %q", name, out.String())
		}
		if !strings.Contains(errOut.String(), tc.want) {
			t.Errorf("%s: stderr %q does not mention %q", name, errOut.String(), tc.want)
		}
	}
}

// The committed CI config runs entirely offline: the live scanner is never
// reached, and the fixture set includes one refusal (VELO's undetermined
// reputation), so the pass exits 1 with one line per asset.
func TestDryRunOverCommittedFixtures(t *testing.T) {
	root := repoRoot(t)
	wd, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	d, out, errOut := testDeps(noNetwork)
	code := run(context.Background(), []string{"--config", ".github/attester/fixtures.json", "--dry-run", "--once"}, d)
	if code != attester.ExitRefused {
		t.Fatalf("exit = %d, want 1; stderr %q", code, errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 8 {
		t.Fatalf("got %d lines, want 8:\n%s", len(lines), out.String())
	}
	if lines[0] != "AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA\tCDJF2JQINO7WRFXB2AAHLONFDPPI4M3W2UM5THGQQ7JMJDIEJYC4CMPG\t0\t0\t2c939ca1692f23f69a91db8475b54cbb349be028d9180ec709acf0648c5ca391" {
		t.Errorf("AQUA line = %q", lines[0])
	}
	if !strings.HasPrefix(lines[6], "VELO-GDM4RQUQQUVSKQA7S6EM7XBZP3FCGH4Q7CL6TABQ7B2BEJ5ERARM2M5M\tCDHI6B6HBY2Q7BVGQ74C23GN3TKPGLCCMOSSRTEQDTTX4ELO5V6A6IOM\trefused\tattest: scan is undetermined") {
		t.Errorf("VELO line = %q", lines[6])
	}
}

func TestLiveDryRunUsesInjectedScanner(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "attested.json")
	if err := os.WriteFile(cfg, []byte(`{"network":"testnet","contract_id":"CBK4FBIHMDTXCUPE4E3ZDVSFJSCY5FJETTKNIQPN4LFJIKKIBLKIXQ73","assets":[{"asset":"USDC-GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var scanned []string
	d, out, _ := testDeps(func(_ context.Context, a mechanics.Asset) (*mechanics.Report, error) {
		scanned = append(scanned, a.String())
		return nil, errors.New("horizon: get /assets: status 503")
	})
	code := run(context.Background(), []string{"--config", cfg, "--dry-run", "--once"}, d)
	if code != attester.ExitOperational {
		t.Fatalf("exit = %d, want 2", code)
	}
	if len(scanned) != 1 {
		t.Fatalf("scanned %v, want the one configured asset", scanned)
	}
	want := "USDC-GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN\tCA2E53VHFZ6YSWQIEIPBXJQGT6VW3VKWWZO555XKRQXYJ63GEBJJGHY7\terror\thorizon: get /assets: status 503\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestDiffCommand(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.tsv")
	head := filepath.Join(dir, "head.tsv")
	_ = os.WriteFile(base, []byte("AQUA-G1\tC1\t0\t0\taaa\n"), 0o600)
	_ = os.WriteFile(head, []byte("AQUA-G1\tC1\t1\t2\tbbb\n"), 0o600)

	d, out, _ := testDeps(noNetwork)
	if code := run(context.Background(), []string{"diff", base, head}, d); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "| `AQUA-G1` | evidence_hash | `aaa` | `bbb` |") {
		t.Fatalf("diff output:\n%s", out.String())
	}

	d, out, _ = testDeps(noNetwork)
	if code := run(context.Background(), []string{"diff", "--base-note", "base predates the attester", "unused", head}, d); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "base predates the attester") {
		t.Fatalf("base note missing:\n%s", out.String())
	}
}
