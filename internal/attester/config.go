// Package attester is the keyless half of the attestation writer specified in
// docs/attestation-writer.md: it loads the attested-set configuration, runs the
// dry-run pass (scan, derive attest() arguments, derive the SAC address, stop
// before signing), diffs two dry-run outputs, and watches attested issuers for
// authorization-flag changes.
//
// Nothing in this package loads, reads or accepts key material, and nothing
// submits a transaction. Signing is out of scope until key custody is settled
// (#145, #150); the absence of a signing path is the point, not a gap.
package attester

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/use-assay/assay/internal/horizon"
	"github.com/use-assay/assay/internal/mechanics"
	"github.com/use-assay/assay/internal/sac"
	"github.com/use-assay/assay/internal/scan"
)

// ErrConfig reports a configuration the attester refuses to run with.
var ErrConfig = errors.New("attester: invalid config")

// Named networks the config may use instead of a full passphrase. The
// passphrases are fixed by the protocol.
var namedNetworks = map[string]string{
	"pubnet":     string(horizon.PublicNet),
	"testnet":    string(horizon.TestNet),
	"futurenet":  "Test SDF Future Network ; October 2022",
	"standalone": "Standalone Network ; February 2017",
}

// DefaultWatchInterval is how often the issuer watcher polls each issuer when
// the config does not say. See docs/attester.md for the Horizon load it implies.
const DefaultWatchInterval = time.Minute

// Config is the attested-set configuration: what to attest, to which registry,
// on which network. It is read from JSON; unknown fields are an error so a
// mistyped key cannot silently fall back to a default.
type Config struct {
	// Network is the registry's network: "testnet", "pubnet", "futurenet",
	// "standalone" or a full passphrase. The SAC address is derived for this
	// network, not the scan's (docs/attestation-writer.md, question 2).
	Network string `json:"network"`
	// ContractID is the registry contract the attestations are for.
	ContractID string `json:"contract_id"`
	// Cadence is the re-attestation schedule, recorded for the operator and
	// the staleness monitor. The dry-run reads but does not act on it.
	Cadence string `json:"cadence,omitempty"`
	// FixturesDir switches scanning to committed fixtures (offline): each
	// asset then names the fixture subject that stands in for its live scan.
	FixturesDir string `json:"fixtures_dir,omitempty"`
	// ScanNetwork is the network a fixture-mode report is bound to, as a live
	// scan binds the network it read. Defaults to pubnet, where the fixtures
	// were captured. Ignored for live scans.
	ScanNetwork string `json:"scan_network,omitempty"`
	// HorizonURL is the Horizon the issuer watcher polls. Defaults to pubnet,
	// the network the attested assets live on.
	HorizonURL string `json:"horizon_url,omitempty"`
	// WatchInterval is the issuer watcher's poll interval, as a Go duration.
	WatchInterval string `json:"watch_interval,omitempty"`
	// Assets is the attested set, in output order.
	Assets []AssetEntry `json:"assets"`
}

// AssetEntry is one asset in the attested set.
type AssetEntry struct {
	// Asset is CODE-ISSUER, as `assay scan` takes it.
	Asset string `json:"asset"`
	// Fixture is the fixture subject used instead of a live scan when the
	// config sets fixtures_dir.
	Fixture string `json:"fixture,omitempty"`
}

// LoadConfig reads and validates a config file.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %w", ErrConfig, path, err)
	}
	return ParseConfig(b)
}

// ParseConfig decodes and validates config JSON.
func ParseConfig(b []byte) (*Config, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) validate() error {
	if _, err := resolveNetwork(c.Network); err != nil {
		return fmt.Errorf("%w: network: %w", ErrConfig, err)
	}
	if !sac.IsContractAddress(c.ContractID) {
		return fmt.Errorf("%w: contract_id %q is not a contract address (C…)", ErrConfig, c.ContractID)
	}
	if c.Cadence != "" {
		if _, err := time.ParseDuration(c.Cadence); err != nil {
			return fmt.Errorf("%w: cadence %q: %w", ErrConfig, c.Cadence, err)
		}
	}
	if c.WatchInterval != "" {
		if d, err := time.ParseDuration(c.WatchInterval); err != nil || d <= 0 {
			return fmt.Errorf("%w: watch_interval %q must be a positive duration", ErrConfig, c.WatchInterval)
		}
	}
	if c.ScanNetwork != "" {
		if _, err := resolveNetwork(c.ScanNetwork); err != nil {
			return fmt.Errorf("%w: scan_network: %w", ErrConfig, err)
		}
	}
	// A run record with zero entries is itself an error (writer design,
	// question 5); a config that can only produce one is refused up front.
	if len(c.Assets) == 0 {
		return fmt.Errorf("%w: assets is empty; an empty run is an error", ErrConfig)
	}
	seen := make(map[string]bool, len(c.Assets))
	for i, e := range c.Assets {
		a, err := scan.ParseAsset(e.Asset)
		if err != nil {
			return fmt.Errorf("%w: assets[%d]: %w", ErrConfig, i, err)
		}
		key := a.String()
		if seen[key] {
			return fmt.Errorf("%w: assets[%d]: %s is listed twice", ErrConfig, i, key)
		}
		seen[key] = true
		if c.FixturesDir != "" && e.Fixture == "" {
			return fmt.Errorf("%w: assets[%d]: %s has no fixture, but fixtures_dir is set", ErrConfig, i, key)
		}
		if c.FixturesDir == "" && e.Fixture != "" {
			return fmt.Errorf("%w: assets[%d]: %s names a fixture, but fixtures_dir is not set", ErrConfig, i, key)
		}
	}
	return nil
}

// RegistryPassphrase is the passphrase of the registry's network.
func (c *Config) RegistryPassphrase() string {
	p, _ := resolveNetwork(c.Network)
	return p
}

// FixtureScanNetwork is the network a fixture-mode report is bound to.
func (c *Config) FixtureScanNetwork() horizon.Network {
	if c.ScanNetwork == "" {
		return horizon.PublicNet
	}
	p, _ := resolveNetwork(c.ScanNetwork)
	return horizon.Network(p)
}

// Interval is the watcher's poll interval.
func (c *Config) Interval() time.Duration {
	if c.WatchInterval == "" {
		return DefaultWatchInterval
	}
	d, _ := time.ParseDuration(c.WatchInterval)
	return d
}

// ParsedAssets returns the configured assets in order. The config has been
// validated, so parsing cannot fail here.
func (c *Config) ParsedAssets() []mechanics.Asset {
	out := make([]mechanics.Asset, 0, len(c.Assets))
	for _, e := range c.Assets {
		a, _ := scan.ParseAsset(e.Asset)
		out = append(out, a)
	}
	return out
}

func resolveNetwork(s string) (string, error) {
	if s == "" {
		return "", errors.New("is required")
	}
	if p, ok := namedNetworks[strings.ToLower(s)]; ok {
		return p, nil
	}
	// Every Stellar network passphrase has the "<name> ; <month year>" shape;
	// anything else is a typo of a name, not a passphrase.
	if strings.Contains(s, " ; ") {
		return s, nil
	}
	return "", fmt.Errorf("%q is neither a known network (pubnet, testnet, futurenet, standalone) nor a passphrase", s)
}
