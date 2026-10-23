package horizon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/use-assay/assay/internal/horizon"
)

// These fuzz targets drive the client's decoders from arbitrary bytes (#290).
//
// Horizon is the one source whose failure is fatal to a scan, and every
// severity Assay reports is read out of the flags these decoders populate. A
// decoder that panics or half-populates a struct would turn a malformed
// response into a wrong capability reading, so each target asserts:
//
//   - The decoder never panics on any body (the fuzzer itself).
//   - Every failure returns no record at all: an error and a nil pointer
//     together, so no partially-populated struct escapes.
//   - Every success is internally complete: the record the ledger echoed
//     matches the asset or account that was asked for, and the decoded flag
//     set carried all four authorization flags — a complete flag set or an
//     error, never a silently-false one.

// bodyTransport answers every request with one recorded response body, so a
// fuzz iteration pays no server setup.
type bodyTransport []byte

func (b bodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(b)),
		Request:    req,
	}, nil
}

func fuzzClient(body []byte) *horizon.Client {
	c := horizon.New("https://horizon.stellar.org")
	c.HTTP = &http.Client{Transport: bodyTransport(body)}
	return c
}

// flagNames are the four authorization flags every record must carry.
var flagNames = [...]string{"auth_required", "auth_revocable", "auth_immutable", "auth_clawback_enabled"}

// requireCompleteFlags asserts, from the raw response bytes rather than
// through the client's own decoder, that the flags object a successful decode
// must have read carried every flag, each a boolean.
func requireCompleteFlags(t *testing.T, flags map[string]json.RawMessage, where string) {
	t.Helper()
	for _, name := range flagNames {
		v, ok := flags[name]
		if !ok {
			t.Fatalf("success returned a %s whose flags object carries no %s: %v", where, name, flags)
		}
		var b bool
		if err := json.Unmarshal(v, &b); err != nil {
			t.Fatalf("success returned a %s whose %s flag is not a boolean: %v", where, name, v)
		}
	}
}

// FuzzAssetDecode fuzzes the /assets decoder.
func FuzzAssetDecode(f *testing.F) {
	// Seeds from the recorded Horizon fixtures the eval set is built on, so
	// the corpus starts at the response shape the decoder exists to read.
	paths, err := filepath.Glob("../mechanics/testdata/*/asset.json")
	if err != nil {
		f.Fatalf("glob fixtures: %v", err)
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			f.Fatalf("read fixture %s: %v", path, err)
		}
		var meta struct {
			Code   string `json:"asset_code"`
			Issuer string `json:"asset_issuer"`
		}
		if err := json.Unmarshal(body, &meta); err != nil {
			f.Fatalf("fixture %s is not a Horizon asset record: %v", path, err)
		}
		f.Add(meta.Code, meta.Issuer, body)
	}
	// Fixed cases: the shape the client's unit tests pin, and the malformed
	// boundaries this target is about.
	const issuer = "GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA"
	f.Add("AQUA", issuer, []byte(horizonAssets("AQUA", issuer)))
	f.Add("AQUA", issuer, []byte(`{"_embedded":{"records":[]}}`))                                                  // no records
	f.Add("AQUA", issuer, []byte(`{not json`))                                                                     // malformed
	f.Add("AQUA", issuer, []byte(`{"_embedded":{"records":[`+assetJSON("WRONG", issuer)+`]}}`))                    // echoed a different asset
	f.Add("AQUA", issuer, []byte(`{"_embedded":{"records":[{"asset_code":"AQUA","asset_issuer":"`+issuer+`"}]}}`)) // record with no flags object
	f.Add("", "", []byte(`{}`))                                                                                    // empty everything

	f.Fuzz(func(t *testing.T, code, issuer string, body []byte) {
		c := fuzzClient(body)
		stat, err := c.Asset(context.Background(), code, issuer)
		if err != nil {
			// Failure evidence, not a partial answer: nothing may escape
			// alongside the error.
			if stat != nil {
				t.Fatalf("Asset returned a record alongside error %v", err)
			}
			return
		}
		if stat == nil {
			t.Fatal("Asset succeeded without a record")
		}
		// The ledger must echo the asset that was asked for; every severity
		// comes from this record's flags, so a record describing another
		// asset would fake both.
		if stat.AssetCode != code || stat.AssetIssuer != issuer {
			t.Fatalf("success returned %s-%s for a query of %s-%s",
				stat.AssetCode, stat.AssetIssuer, code, issuer)
		}
		// Flag set complete or error, verified from the raw bytes so the
		// assertion does not depend on the client's own decoder.
		var page struct {
			Embedded struct {
				Records []struct {
					Flags map[string]json.RawMessage `json:"flags"`
				} `json:"records"`
			} `json:"_embedded"`
		}
		if jerr := json.Unmarshal(body, &page); jerr != nil {
			t.Fatalf("the client accepted a body the generic decoder rejects: %v", jerr)
		}
		if len(page.Embedded.Records) != 1 {
			t.Fatalf("success decoded %d records, want exactly one", len(page.Embedded.Records))
		}
		requireCompleteFlags(t, page.Embedded.Records[0].Flags, "asset record")
	})
}

// FuzzAccountDecode fuzzes the /accounts decoder.
func FuzzAccountDecode(f *testing.F) {
	// Seeds from the recorded Horizon account fixtures.
	paths, err := filepath.Glob("../mechanics/testdata/*/account.json")
	if err != nil {
		f.Fatalf("glob fixtures: %v", err)
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			f.Fatalf("read fixture %s: %v", path, err)
		}
		var meta struct {
			AccountID string `json:"account_id"`
		}
		if err := json.Unmarshal(body, &meta); err != nil {
			f.Fatalf("fixture %s is not a Horizon account record: %v", path, err)
		}
		f.Add(meta.AccountID, body)
	}
	// Fixed cases: the pinned response shape, and the malformed boundaries —
	// a record with no flags object at all, and one with a partial flags
	// object, must both be decode errors rather than all-false flag sets.
	const id = "GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA"
	f.Add(id, []byte(horizonAccount(id)))
	f.Add(id, []byte(`{}`))                                                                                                                                            // no flags object
	f.Add(id, []byte(fmt.Sprintf(`{"account_id":%q}`, id)))                                                                                                            // no flags object, echoed id
	f.Add(id, []byte(fmt.Sprintf(`{"account_id":%q,"flags":{"auth_required":false}}`, id)))                                                                            // partial flags
	f.Add(id, []byte(fmt.Sprintf(`{"account_id":%q,"flags":{}}`, id)))                                                                                                 // empty flags
	f.Add(id, []byte(fmt.Sprintf(`{"account_id":%q,"flags":null}`, id)))                                                                                               // null flags
	f.Add(id, []byte(fmt.Sprintf(`{"account_id":%q,"flags":{"auth_required":false,"auth_revocable":false,"auth_immutable":false,"auth_clawback_enabled":"no"}}`, id))) // non-boolean flag
	f.Add(id, []byte(`{not json`))                                                                                                                                     // malformed
	f.Add("", []byte(`{}`))

	f.Fuzz(func(t *testing.T, id string, body []byte) {
		c := fuzzClient(body)
		acct, err := c.Account(context.Background(), id)
		if err != nil {
			// Failure evidence, not a partial answer: nothing may escape
			// alongside the error.
			if acct != nil {
				t.Fatalf("Account returned a record alongside error %v", err)
			}
			return
		}
		if acct == nil {
			t.Fatal("Account succeeded without a record")
		}
		// Asking for one account cannot be answered by another: the record
		// must echo the ID that was requested, or every flag read on it
		// belongs to a different account.
		if acct.AccountID != id {
			t.Fatalf("success returned account %s for a query of %s", acct.AccountID, id)
		}
		// Flag set complete or error, verified from the raw bytes so the
		// assertion does not depend on the client's own decoder.
		var probe struct {
			Flags map[string]json.RawMessage `json:"flags"`
		}
		if jerr := json.Unmarshal(body, &probe); jerr != nil {
			t.Fatalf("the client accepted a body the generic decoder rejects: %v", jerr)
		}
		requireCompleteFlags(t, probe.Flags, "account record")
	})
}
