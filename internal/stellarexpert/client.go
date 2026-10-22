// Package stellarexpert consumes StellarExpert's curated reputation data.
//
// Assay does not build a reputation layer and does not second-guess this one.
// Everything this package returns is passed through to the report as attributed
// evidence, carrying the source URL and retrieval time. Nothing here is
// re-derived, re-scored, or restated as an Assay conclusion.
//
// Endpoints verified live against api.stellar.expert:
//
//	GET /explorer/directory/{address}                 -> curated address entry
//	GET /explorer/directory/blocked-domains/{domain}  -> {"domain":..,"blocked":bool}
//
// This is an extraction candidate for a shared ledger-access library.
package stellarexpert

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// DefaultURL is the public StellarExpert API root.
const DefaultURL = "https://api.stellar.expert"

// version is kept here so every outbound client reports the same tool
// version in its User-Agent. Bump it alongside any scanner-version release;
// the API documents the value in release notes.
const version = "v0.1.0"

// defaultUserAgent is how Assay identifies itself to the public StellarExpert
// API. It is a courtesy to the operators of sources Assay depends on and makes
// automated traffic distinguishable from botnets and scanners.
const defaultUserAgent = "assay/" + version + " (+https://github.com/use-assay/Assay)"

// MaxBody caps a curated-endpoint response. Real responses are a few KB; like
// the SEP-1 toml cap this stops a broken or hostile server from streaming an
// unbounded body at the scanner. The decode reads at most this many bytes, so
// an oversized document is truncated and then fails to decode rather than
// allocating without bound. It is exported so the resource-exhaustion tests
// can assert the bound rather than assume it.
const MaxBody = 1 << 20 // 1 MiB

// DirectoryEntry is a curated entry from StellarExpert's address directory,
// the data set standardized by SEP-0037.
type DirectoryEntry struct {
	Address string   `json:"address"`
	Name    string   `json:"name"`
	Domain  string   `json:"domain"`
	Tags    []string `json:"tags"`
}

// HasTag reports whether the entry carries the given tag.
func (e *DirectoryEntry) HasTag(tag string) bool {
	if e == nil {
		return false
	}
	for _, t := range e.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// BlockedDomain is the response from the blocked-domains lookup.
type BlockedDomain struct {
	Domain  string `json:"domain"`
	Blocked bool   `json:"blocked"`
}

// Client reads StellarExpert's curated data sets.
type Client struct {
	BaseURL   string
	HTTP      *http.Client
	UserAgent string
}

// New returns a Client for a given API root, defaulting to the public one.
func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultURL
	}
	return &Client{
		BaseURL:   baseURL,
		HTTP:      &http.Client{Timeout: 15 * time.Second},
		UserAgent: defaultUserAgent,
	}
}

// DirectoryURL returns the public URL for an address's directory entry, used
// to attribute the claim in the report.
func (c *Client) DirectoryURL(address string) string {
	return c.BaseURL + "/explorer/directory/" + url.PathEscape(address)
}

// BlockedDomainURL returns the public URL for a blocked-domain lookup.
func (c *Client) BlockedDomainURL(domain string) string {
	return c.BaseURL + "/explorer/directory/blocked-domains/" + url.PathEscape(domain)
}

// Directory looks up an address in the curated directory. A nil entry with a
// nil error means the address is simply not listed, which is the common case
// and is not itself a signal in either direction.
func (c *Client) Directory(ctx context.Context, address string) (*DirectoryEntry, error) {
	var e DirectoryEntry
	found, err := c.get(ctx, c.DirectoryURL(address), &e)
	if err != nil || !found {
		return nil, err
	}

	// The directory answers an address it holds no entry for with 200 and an
	// empty object, not a 404 — verified against several unlisted addresses.
	// So a successful decode is not by itself a listing. A real entry always
	// echoes the address it describes, which is the discriminator used here.
	//
	// Without this, an absent entry surfaces downstream as the attributed
	// claim `listed as "" (domain "", tags: )` — a statement this source never
	// made, attached to its name and URL.
	if e.Address == "" {
		return nil, nil
	}
	return &e, nil
}

// BlockedDomain reports whether a domain appears on the malicious-domain
// blocklist.
func (c *Client) BlockedDomain(ctx context.Context, domain string) (*BlockedDomain, error) {
	var b BlockedDomain
	found, err := c.get(ctx, c.BlockedDomainURL(domain), &b)
	if err != nil || !found {
		return nil, err
	}
	if b.Domain == "" {
		return nil, nil
	}
	return &b, nil
}

// get returns found=false on 404 rather than an error, because "not listed" is
// a normal answer from these endpoints.
func (c *Client) get(ctx context.Context, target string, out any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false, fmt.Errorf("stellarexpert: get %s: %w", target, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("stellarexpert: get %s: status %d", target, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody))
	if err != nil {
		return false, fmt.Errorf("stellarexpert: read %s: %w", target, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return false, fmt.Errorf("stellarexpert: decode %s: %w", target, err)
	}
	return true, nil
}
