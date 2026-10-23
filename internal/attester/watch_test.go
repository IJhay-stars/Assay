package attester

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/use-assay/assay/internal/horizon"
)

const (
	watchedIssuer = "GA22QHSHQEHDJS2ZOINSC77XPPQ24G5EFRJGVEIZLKC5FAW3PQ5XNSDQ"
	otherAccount  = "GB7TAYRUZGE6TVT7NHP5SMIZRNQA6PLM423EYISAOAP3MKYIQMVYP2JO"
)

func TestApplyOperation(t *testing.T) {
	start := horizon.Flags{AuthRequired: true}
	for name, tc := range map[string]struct {
		op          horizon.Operation
		want        horizon.Flags
		wantChanged bool
	}{
		"not set_options": {
			horizon.Operation{Type: "payment", TransactionSuccessful: true, SourceAccount: watchedIssuer},
			start, false,
		},
		"failed transaction": {
			horizon.Operation{Type: "set_options", SetFlags: []int{8}, SourceAccount: watchedIssuer},
			start, false,
		},
		"another account's set_options": {
			horizon.Operation{Type: "set_options", TransactionSuccessful: true, SetFlags: []int{8}, SourceAccount: otherAccount},
			start, false,
		},
		"no-op: sets a flag already set": {
			horizon.Operation{Type: "set_options", TransactionSuccessful: true, SetFlags: []int{1}, SourceAccount: watchedIssuer},
			start, false,
		},
		"no-op: clears a flag already clear": {
			horizon.Operation{Type: "set_options", TransactionSuccessful: true, ClearFlags: []int{8}, SourceAccount: watchedIssuer},
			start, false,
		},
		"sets clawback": {
			horizon.Operation{Type: "set_options", TransactionSuccessful: true, SetFlags: []int{8}, SourceAccount: watchedIssuer},
			horizon.Flags{AuthRequired: true, AuthClawbackEnabled: true}, true,
		},
		"clears required, sets revocable and immutable": {
			horizon.Operation{Type: "set_options", TransactionSuccessful: true, ClearFlags: []int{1}, SetFlags: []int{2, 4}, SourceAccount: watchedIssuer},
			horizon.Flags{AuthRevocable: true, AuthImmutable: true}, true,
		},
	} {
		got, changed := ApplyOperation(start, watchedIssuer, tc.op)
		if got != tc.want || changed != tc.wantChanged {
			t.Errorf("%s: got %+v changed=%v, want %+v changed=%v", name, got, changed, tc.want, tc.wantChanged)
		}
	}
}

// fakeHorizon serves a fixture operation stream for one issuer.
type fakeHorizon struct {
	mu       sync.Mutex
	pages    map[string][]byte // cursor -> page served for an ascending request
	requests []string
}

func newFixtureHorizon(t *testing.T) (*httptest.Server, *fakeHorizon) {
	t.Helper()
	page, err := os.ReadFile("testdata/operations-after-cursor.json")
	if err != nil {
		t.Fatal(err)
	}
	empty := []byte(`{"_embedded":{"records":[]}}`)
	f := &fakeHorizon{pages: map[string][]byte{
		"250000000000000000": page,
		"250000000000000005": empty,
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.URL.RequestURI())
		f.mu.Unlock()
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/accounts/"+watchedIssuer:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"account_id": watchedIssuer,
				// Horizon always publishes all four flags, and the decoder
				// refuses a partial set (#530), so the fixture does too.
				"flags": map[string]bool{
					"auth_required":         true,
					"auth_revocable":        false,
					"auth_immutable":        false,
					"auth_clawback_enabled": false,
				},
			})
		case r.URL.Path == "/accounts/"+watchedIssuer+"/operations" && q.Get("order") == "desc":
			_, _ = w.Write([]byte(`{"_embedded":{"records":[{"id":"250000000000000000","paging_token":"250000000000000000","type":"payment","transaction_successful":true}]}}`))
		case r.URL.Path == "/accounts/"+watchedIssuer+"/operations":
			body, ok := f.pages[q.Get("cursor")]
			if !ok {
				http.Error(w, "unexpected cursor "+q.Get("cursor"), http.StatusBadRequest)
				return
			}
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

func watchConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := ParseConfig([]byte(`{
		"network": "testnet",
		"contract_id": "CBK4FBIHMDTXCUPE4E3ZDVSFJSCY5FJETTKNIQPN4LFJIKKIBLKIXQ73",
		"assets": [
			{"asset": "BERKSHIRE-GA22QHSHQEHDJS2ZOINSC77XPPQ24G5EFRJGVEIZLKC5FAW3PQ5XNSDQ"},
			{"asset": "BRK-GA22QHSHQEHDJS2ZOINSC77XPPQ24G5EFRJGVEIZLKC5FAW3PQ5XNSDQ"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestWatcherDetectsFlagChangeOnFixtureStream(t *testing.T) {
	srv, fake := newFixtureHorizon(t)
	w := NewWatcher(watchConfig(t), horizon.New(srv.URL))
	ctx := context.Background()
	if err := w.Init(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := w.Poll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []FlagTransition{{
		Issuer: watchedIssuer,
		Assets: []string{
			"BERKSHIRE-" + watchedIssuer,
			"BRK-" + watchedIssuer,
		},
		From:        horizon.Flags{AuthRequired: true},
		To:          horizon.Flags{AuthRequired: true, AuthRevocable: true, AuthClawbackEnabled: true},
		Added:       []string{"auth_clawback_enabled", "auth_revocable"},
		OperationID: "250000000000000005",
		TxHash:      "aa00000000000000000000000000000000000000000000000000000000000005",
		At:          "2026-10-20T09:20:00Z",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("transitions:\n got %+v\nwant %+v", got, want)
	}

	// The cursor advanced past the whole page: the next poll asks from the
	// last operation and, with nothing new, reports nothing.
	again, err := w.Poll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("second poll reported %+v, want nothing", again)
	}

	// One issuer backs both assets, so it is polled once per pass, and no
	// request ever relies on Horizon's unreliable server-side type filter.
	for _, req := range fake.requests {
		if strings.Contains(req, "type=") {
			t.Errorf("request %s uses a server-side type filter", req)
		}
	}
	var opsRequests int
	for _, req := range fake.requests {
		if strings.Contains(req, "/operations?") && strings.Contains(req, "order=asc") {
			opsRequests++
		}
	}
	if opsRequests != 2 {
		t.Errorf("ascending operations requests = %d, want 2 (one per poll)", opsRequests)
	}
}

func TestWatcherKeepsCursorWhenPollFails(t *testing.T) {
	srv, fake := newFixtureHorizon(t)
	w := NewWatcher(watchConfig(t), horizon.New(srv.URL))
	ctx := context.Background()
	if err := w.Init(ctx); err != nil {
		t.Fatal(err)
	}

	// Horizon fails for the first poll: nothing is reported and nothing is
	// skipped, because the cursor does not move.
	fake.mu.Lock()
	page := fake.pages["250000000000000000"]
	delete(fake.pages, "250000000000000000")
	fake.mu.Unlock()
	if _, err := w.Poll(ctx); err == nil {
		t.Fatal("poll against a failing Horizon returned no error")
	}

	fake.mu.Lock()
	fake.pages["250000000000000000"] = page
	fake.mu.Unlock()
	got, err := w.Poll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].OperationID != "250000000000000005" {
		t.Fatalf("after recovery got %+v, want the one flag change", got)
	}
}
