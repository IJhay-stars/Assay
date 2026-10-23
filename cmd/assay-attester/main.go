// Command assay-attester is the keyless half of the attestation writer
// designed in docs/attestation-writer.md.
//
//	assay-attester --config PATH --dry-run --once   scan, derive, print; never sign
//	assay-attester --config PATH --watch            report issuer flag changes as they land
//	assay-attester diff BASE HEAD                   compare two dry-run outputs (Markdown)
//
// There is no signing mode. Running without --dry-run or --watch is refused:
// key custody (#150) and the re-attestation runbook (#145) must land before
// any code path here touches a key, so none exists. See docs/attester.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/use-assay/assay/internal/attester"
	"github.com/use-assay/assay/internal/horizon"
	"github.com/use-assay/assay/internal/mechanics"
	"github.com/use-assay/assay/internal/scan"
)

// perAssetTimeout bounds one live scan, as `assay scan` does.
const perAssetTimeout = 30 * time.Second

// errUsage marks argument errors; they exit 2 like operational failures, as
// the design groups "config" with operational failure.
var errUsage = errors.New("usage")

type deps struct {
	stdout, stderr io.Writer
	// liveScan scans one asset against the production sources. Injected so
	// tests never touch the network.
	liveScan attester.ScanFunc
	// horizon builds the watcher's Horizon reader for a base URL.
	horizon func(baseURL string) attester.HorizonReader
}

func main() {
	d := deps{
		stdout: os.Stdout,
		stderr: os.Stderr,
		liveScan: func(ctx context.Context, a mechanics.Asset) (*mechanics.Report, error) {
			return scan.New().Scan(ctx, a)
		},
		horizon: func(baseURL string) attester.HorizonReader { return horizon.New(baseURL) },
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], d))
}

func run(ctx context.Context, args []string, d deps) int {
	if len(args) > 0 && args[0] == "diff" {
		return runDiff(args[1:], d)
	}

	fs := flag.NewFlagSet("assay-attester", flag.ContinueOnError)
	fs.SetOutput(d.stderr)
	configPath := fs.String("config", "", "attested-set config (asset list, network, contract ID, cadence)")
	dryRun := fs.Bool("dry-run", false, "scan, derive and print per asset; never sign")
	once := fs.Bool("once", false, "one pass, then exit (the unit a scheduler invokes)")
	watch := fs.Bool("watch", false, "watch attested issuers and report authorization-flag changes (read-only)")
	if err := fs.Parse(args); err != nil {
		return attester.ExitOperational
	}

	if err := checkModes(fs.NArg(), *configPath, *dryRun, *once, *watch); err != nil {
		_, _ = fmt.Fprintln(d.stderr, "assay-attester:", err)
		return attester.ExitOperational
	}

	cfg, err := attester.LoadConfig(*configPath)
	if err != nil {
		_, _ = fmt.Fprintln(d.stderr, "assay-attester:", err)
		return attester.ExitOperational
	}

	if *watch {
		return runWatch(ctx, cfg, d)
	}
	return runDryRun(ctx, cfg, d)
}

func checkModes(nargs int, configPath string, dryRun, once, watch bool) error {
	switch {
	case nargs != 0:
		return fmt.Errorf("%w: unexpected arguments", errUsage)
	case configPath == "":
		return fmt.Errorf("%w: --config is required", errUsage)
	case watch && (dryRun || once):
		return fmt.Errorf("%w: --watch runs on its own; it cannot be combined with --dry-run or --once", errUsage)
	case watch:
		return nil
	case !dryRun:
		return fmt.Errorf("%w: only --dry-run and --watch are implemented; signing is out of scope until key custody is settled (#145, #150)", errUsage)
	case !once:
		return fmt.Errorf("%w: scheduled runs are not implemented; pass --once and let the scheduler repeat it", errUsage)
	}
	return nil
}

func runDryRun(ctx context.Context, cfg *attester.Config, d deps) int {
	scanFn := d.liveScan
	if cfg.FixturesDir != "" {
		scanFn = attester.FixtureScan(cfg)
	}
	bounded := func(ctx context.Context, a mechanics.Asset) (*mechanics.Report, error) {
		ctx, cancel := context.WithTimeout(ctx, perAssetTimeout)
		defer cancel()
		return scanFn(ctx, a)
	}

	results := attester.DryRun(ctx, cfg, bounded)
	for _, r := range results {
		_, _ = fmt.Fprintln(d.stdout, attester.FormatResult(r))
	}
	return attester.ExitCode(results)
}

func runWatch(ctx context.Context, cfg *attester.Config, d deps) int {
	log := slog.New(slog.NewJSONHandler(d.stdout, nil))
	baseURL := cfg.HorizonURL
	if baseURL == "" {
		baseURL = horizon.DefaultURL
	}
	w := attester.NewWatcher(cfg, d.horizon(baseURL))
	if err := w.Init(ctx); err != nil {
		_, _ = fmt.Fprintln(d.stderr, "assay-attester:", err)
		return attester.ExitOperational
	}
	log.Info("watching attested issuers", "horizon", baseURL, "interval", cfg.Interval().String(), "assets", len(cfg.Assets))

	err := w.Run(ctx, cfg.Interval(),
		func(t attester.FlagTransition) {
			log.Warn("capability transition",
				"issuer", t.Issuer, "assets", t.Assets,
				"added", t.Added, "removed", t.Removed,
				"from", t.From, "to", t.To,
				"operation_id", t.OperationID, "transaction_hash", t.TxHash, "at", t.At)
		},
		func(err error) { log.Error("poll failed", "error", err.Error()) },
	)
	if errors.Is(err, context.Canceled) {
		return 0
	}
	_, _ = fmt.Fprintln(d.stderr, "assay-attester:", err)
	return attester.ExitOperational
}

func runDiff(args []string, d deps) int {
	fs := flag.NewFlagSet("assay-attester diff", flag.ContinueOnError)
	fs.SetOutput(d.stderr)
	baseNote := fs.String("base-note", "", "explain why there is no base to compare against; BASE is then ignored")
	if err := fs.Parse(args); err != nil || fs.NArg() != 2 {
		_, _ = fmt.Fprintln(d.stderr, "usage: assay-attester diff [--base-note TEXT] BASE HEAD")
		return attester.ExitOperational
	}
	head, err := readOutput(fs.Arg(1))
	if err != nil {
		_, _ = fmt.Fprintln(d.stderr, "assay-attester: head:", err)
		return attester.ExitOperational
	}
	var changes []attester.FieldChange
	if *baseNote == "" {
		base, err := readOutput(fs.Arg(0))
		if err != nil {
			_, _ = fmt.Fprintln(d.stderr, "assay-attester: base:", err)
			return attester.ExitOperational
		}
		changes = attester.Diff(base, head)
	}
	_, _ = fmt.Fprint(d.stdout, attester.RenderMarkdown(changes, head, *baseNote))
	return 0
}

func readOutput(path string) (map[string]attester.Line, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	lines, err := attester.ParseOutput(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return lines, nil
}
