package assetlist_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/use-assay/assay/internal/assetlist"
)

// FuzzFetch drives the list parser from arbitrary bytes (#292).
//
// A list URL is operator configuration, but the document behind it is remote:
// a broken or hostile publisher must produce failure evidence — an error — and
// never a panic or a half-decoded list. A partial list is the dangerous shape,
// because an entry that decoded halfway looks exactly like an entry the
// publisher described that way, and the report would present the parser's
// truncation as the provider's curation.
//
// The invariants:
//
//   - The parser never panics on any body (the fuzzer itself).
//   - Every failure returns no list at all: an error and a nil list together,
//     so a caller cannot mistake "this provider could not be read" for a
//     decoded answer.
//   - Every success is a whole-list decode: the body is one JSON object the
//     generic decoder agrees with field for field, carrying the list's own
//     name, with provenance recorded. A parse that stopped partway cannot
//     report success.
func FuzzFetch(f *testing.F) {
	// Seed from the real published list so the corpus starts at the document
	// shape the parser exists to read, not only at degenerate ones.
	seed, err := os.ReadFile(fixture)
	if err != nil {
		f.Fatalf("read seed fixture: %v", err)
	}
	f.Add(seed)
	// Fixed malformed cases, including every rejection TestFetchRejectsNonLists
	// pins, so the fuzzer starts from the boundary the unit tests describe.
	f.Add([]byte(`{}`))                                           // decodes, but has no name
	f.Add([]byte(`{"name":`))                                     // truncated
	f.Add([]byte(`[]`))                                           // not an object
	f.Add([]byte(`"a string"`))                                   // not an object
	f.Add([]byte(`{"name":"x","assets":[]}`))                     // minimal valid list
	f.Add([]byte(`{"name":"x","assets":[{"decimals":"seven"}]}`)) // wrong type inside an entry

	f.Fuzz(func(t *testing.T, body []byte) {
		c := clientFor(io.NopCloser(bytes.NewReader(body)))
		list, err := c.Fetch(context.Background(), "https://example.test/list.json")

		if err != nil {
			// Failure evidence, not a partial answer: nothing may escape
			// alongside the error.
			if list != nil {
				t.Fatalf("Fetch returned a list alongside error %v", err)
			}
			return
		}
		if list == nil {
			t.Fatal("Fetch succeeded without a list")
		}
		// The one required field: without a name the document is not a SAL,
		// and an accepted nameless list would render as "this provider lists
		// nothing".
		if list.Name == "" {
			t.Fatal("Fetch accepted a list with no name")
		}
		if list.URL == "" || list.FetchedAt.IsZero() {
			t.Fatalf("accepted list is missing provenance: url=%q fetchedAt=%v", list.URL, list.FetchedAt)
		}

		// Whole-list or error: the accepted body must decode, as a whole,
		// into the format's own shape, and every format field the client kept
		// must be what a generic decode of the same bytes sees.
		var ref assetlist.List
		if jerr := json.Unmarshal(body, &ref); jerr != nil {
			t.Fatalf("Fetch accepted a body the generic decoder rejects: %v", jerr)
		}
		got, merr1 := json.Marshal(list)
		want, merr2 := json.Marshal(&ref)
		if merr1 != nil || merr2 != nil {
			t.Fatalf("re-encoding a decoded list failed: %v / %v", merr1, merr2)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("Fetch kept a partial list:\nclient:  %s\ngeneric: %s", got, want)
		}
	})
}
