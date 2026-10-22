package stellarexpert_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/use-assay/assay/internal/stellarexpert"
)

// These drive the client from recorded response bytes rather than hand-built
// structs, because the behaviour under test is how this source actually
// answers — a fixture built from the package's own types could only prove the
// decoder agrees with itself.
//
// Bodies captured 2026-09-05 from api.stellar.expert.
const (
	// An address the directory holds no entry for. Note the 200: this endpoint
	// does not 404 for an unknown address.
	bodyUnlisted = `{}`
	// A real entry, which echoes the address it describes.
	bodyListed = `{"address":"GAROH4EV3WVVTRQKEY43GZK3XSRBEYETRVZ7SVG5LHWOAANSMCTJBB3U","name":"Zeam.Money","domain":"zeam.money","tags":["issuer"]}`
)

// TestUserAgent pins the identity the scanner offers StellarExpert. A specific
// string, not merely a non-empty one, because operators of the services we
// call filter on it.
func TestUserAgent(t *testing.T) {
	want := "assay/v0.1.0 (+https://github.com/use-assay/Assay)"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != want {
			t.Errorf("User-Agent = %q, want %q", got, want)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(bodyUnlisted))
	}))
	t.Cleanup(srv.Close)

	c := stellarexpert.New(srv.URL)
	if _, err := c.Directory(context.Background(), "GBBS25EGYQPGEZCGCFBKG4OAGFXU6DSOQBGTHELLJT3HZXZJ34HWS6XV"); err != nil {
		t.Fatalf("Directory: %v", err)
	}
}

func serve(t *testing.T, status int, body string) *stellarexpert.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return stellarexpert.New(srv.URL)
}

// The bug this test exists for: 200 with an empty object was read as a listing,
// and surfaced downstream as the attributed claim `listed as ""` — a statement
// the directory never made.
func TestEmptyDirectoryResponseIsNotAListing(t *testing.T) {
	entry, err := serve(t, http.StatusOK, bodyUnlisted).
		Directory(context.Background(), "GBBS25EGYQPGEZCGCFBKG4OAGFXU6DSOQBGTHELLJT3HZXZJ34HWS6XV")
	if err != nil {
		t.Fatalf("an empty entry is a normal answer, not an error: %v", err)
	}
	if entry != nil {
		t.Fatalf("empty response reported as a listing: %+v", entry)
	}
}

func TestRealDirectoryEntryIsReturned(t *testing.T) {
	entry, err := serve(t, http.StatusOK, bodyListed).
		Directory(context.Background(), "GAROH4EV3WVVTRQKEY43GZK3XSRBEYETRVZ7SVG5LHWOAANSMCTJBB3U")
	if err != nil {
		t.Fatalf("Directory: %v", err)
	}
	if entry == nil {
		t.Fatal("a real entry was dropped")
	}
	if entry.Name != "Zeam.Money" || entry.Domain != "zeam.money" {
		t.Fatalf("entry decoded wrongly: %+v", entry)
	}
	if !entry.HasTag("issuer") {
		t.Errorf("tags lost: %v", entry.Tags)
	}
}

func TestNotFoundIsNotAnError(t *testing.T) {
	entry, err := serve(t, http.StatusNotFound, `{}`).
		Directory(context.Background(), "GBBS25EGYQPGEZCGCFBKG4OAGFXU6DSOQBGTHELLJT3HZXZJ34HWS6XV")
	if err != nil {
		t.Fatalf("404 means not listed, which is a normal answer: %v", err)
	}
	if entry != nil {
		t.Fatalf("404 reported as a listing: %+v", entry)
	}
}

// The distinction the whole degraded-scan handling rests on: an outage must be
// an error so the caller can tell it from an answer. If these ever collapse,
// a rate-limit becomes a clean bill of health again.
func TestOutageIsAnErrorNotAnAbsentEntry(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		_, err := serve(t, status, `{}`).
			Directory(context.Background(), "GBBS25EGYQPGEZCGCFBKG4OAGFXU6DSOQBGTHELLJT3HZXZJ34HWS6XV")
		if err == nil {
			t.Errorf("status %d was reported as an absent entry rather than a failure", status)
		}
	}
}

func TestBlockedDomainDecodes(t *testing.T) {
	c := serve(t, http.StatusOK, `{"domain":"darkpool.digital","blocked":false}`)
	b, err := c.BlockedDomain(context.Background(), "darkpool.digital")
	if err != nil {
		t.Fatalf("BlockedDomain: %v", err)
	}
	if b == nil || b.Domain != "darkpool.digital" || b.Blocked {
		t.Fatalf("blocklist answer decoded wrongly: %+v", b)
	}
}

func TestEmptyBlockedDomainResponseIsNotListed(t *testing.T) {
	b, err := serve(t, http.StatusOK, `{}`).
		BlockedDomain(context.Background(), "unknown.example")
	if err != nil {
		t.Fatalf("an empty response is a normal answer, not an error: %v", err)
	}
	if b != nil {
		t.Fatalf("empty response reported as a blocklist result: %+v", b)
	}
}

type responseClass uint8

const (
	responseFailed responseClass = iota
	responseNotListed
	responseListed
)

func classifyResponse(found bool, err error) responseClass {
	if err != nil {
		return responseFailed
	}
	if !found {
		return responseNotListed
	}
	return responseListed
}

func fuzzClient(status uint16, body []byte) *stellarexpert.Client {
	c := stellarexpert.New("https://example.test")
	c.HTTP = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: int(status),
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
	return c
}

func assertResponseClass(t *testing.T, class responseClass, err error) {
	t.Helper()
	switch class {
	case responseListed:
		if err != nil {
			t.Fatalf("listed response also returned an error: %v", err)
		}
	case responseNotListed:
		if err != nil {
			t.Fatalf("not-listed response also returned an error: %v", err)
		}
	case responseFailed:
		if err == nil {
			t.Fatal("failed response did not return an error")
		}
	default:
		t.Fatalf("response has unrecognized classification %d", class)
	}
}

func FuzzDirectoryClassification(f *testing.F) {
	directoryFixture, err := os.ReadFile(filepath.Join("..", "mechanics", "testdata", "aqua-clear-verified", "directory.json"))
	if err != nil {
		f.Fatalf("read directory fixture: %v", err)
	}
	f.Add(uint16(http.StatusOK), directoryFixture)
	f.Add(uint16(http.StatusOK), []byte(bodyUnlisted))
	f.Add(uint16(http.StatusOK), []byte("{"))
	f.Add(uint16(http.StatusNotFound), []byte("{}"))

	f.Fuzz(func(t *testing.T, status uint16, body []byte) {
		entry, err := fuzzClient(status, body).Directory(context.Background(), address)
		class := classifyResponse(entry != nil, err)
		assertResponseClass(t, class, err)
		if class == responseListed && entry.Address == "" {
			t.Fatal("listed response has no address")
		}
	})
}

func FuzzBlockedDomainClassification(f *testing.F) {
	blockedFixture, err := os.ReadFile(filepath.Join("..", "mechanics", "testdata", "aqua-clear-verified", "blocked.json"))
	if err != nil {
		f.Fatalf("read blocked-domain fixture: %v", err)
	}
	f.Add(uint16(http.StatusOK), blockedFixture)
	f.Add(uint16(http.StatusOK), []byte(`{"domain":"aqua.network","blocked":false}`))
	f.Add(uint16(http.StatusOK), []byte(`{}`))
	f.Add(uint16(http.StatusOK), []byte("{"))
	f.Add(uint16(http.StatusNotFound), []byte("{}"))

	f.Fuzz(func(t *testing.T, status uint16, body []byte) {
		result, err := fuzzClient(status, body).BlockedDomain(context.Background(), "aqua.network")
		class := classifyResponse(result != nil, err)
		assertResponseClass(t, class, err)
		if class == responseListed && result.Domain == "" {
			t.Fatal("listed response has no domain")
		}
	})
}
