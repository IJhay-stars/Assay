package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/use-assay/assay/internal/differential"
	"github.com/use-assay/assay/internal/horizon"
	"github.com/use-assay/assay/internal/mechanics"
)

// clearAccountEntryXDR is the raw ACCOUNT LedgerEntryData for the AQUA issuer
// with no inflationDest and flags 0. It is the same fixture pinned by
// internal/differential/xdr_test.go; the differential tests there validate the
// offsets this one mutates.
const clearAccountEntryXDR = "AAAAAAAAAABblC5TrDPI/QqAzHwbGoXX2DipxBl3qtGLOvBX+OM98AAAAAAAAAABAAAAAAAAAAEAAAAAAAAAAAAAAAAAAAAMYXF1YS5uZXR3b3JrAAAAAAAAAAAAAAAA"

// differentialRPC serves getLedgerEntries for the test issuer. flags rewrites
// the four flag bytes in the pinned fixture, so every comparison state can be
// produced without a network. A non-zero status makes the RPC answer fail.
func differentialRPC(t *testing.T, flags uint32, status int) *httptest.Server {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(clearAccountEntryXDR)
	if err != nil {
		t.Fatalf("decode pinned account XDR: %v", err)
	}
	binary.BigEndian.PutUint32(raw[64:], flags)
	xdr := base64.StdEncoding.EncodeToString(raw)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		var req struct {
			Params struct {
				Keys []string `json:"keys"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		key := ""
		if len(req.Params.Keys) > 0 {
			key = req.Params.Keys[0]
		}
		out, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"entries": []map[string]string{{"key": key, "xdr": xdr}},
			},
		})
		if err != nil {
			t.Errorf("marshal fake RPC response: %v", err)
			return
		}
		_, _ = w.Write(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func verifySubject(flags horizon.Flags) *mechanics.Subject {
	return &mechanics.Subject{
		Asset:  mechanics.Asset{Code: verifyTestAssetCode, Issuer: verifyTestAssetIssuer},
		Stat:   &horizon.AssetStat{Flags: flags},
		Issuer: &horizon.Account{AccountID: verifyTestAssetIssuer, Flags: flags},
	}
}

// TestVerifyDifferentialStates is the acceptance test for #301: with
// -differential, the command reports each of docs/differential.md's four
// states, and a disagreement can never exit 0. The RPC fixtures are hermetic
// and derived from the XDR fixture already pinned in internal/differential.
func TestVerifyDifferentialStates(t *testing.T) {
	asset := mechanics.Asset{Code: verifyTestAssetCode, Issuer: verifyTestAssetIssuer}
	rep, params := attestableReport(t, nil)
	chain := chainFrom(params, verifyTableTime.Add(-time.Hour).Unix(), false)
	read := func(mechanics.Asset) (*OnChainSafety, error) { return chain, nil }

	run := func(subject *mechanics.Subject, rpcURL string) *VerifyResult {
		t.Helper()
		res, err := verifyAsset(context.Background(), asset, 0, verifyTableTime,
			func(context.Context, mechanics.Asset) (*mechanics.Report, *mechanics.Subject, error) {
				return rep, subject, nil
			},
			read, rpcURL)
		if err != nil {
			t.Fatalf("verifyAsset: %v", err)
		}
		if res.Differential == nil {
			t.Fatal("differential result missing even though an RPC URL was supplied")
		}
		return res
	}

	t.Run("agreement", func(t *testing.T) {
		srv := differentialRPC(t, 0, 0)
		res := run(verifySubject(horizon.Flags{}), srv.URL)
		if res.Differential.State != differential.StateAgreement {
			t.Fatalf("state = %q (%s), want agreement", res.Differential.State, res.Differential.Reason)
		}
		if code := verifyExitCode(res); code != exitSuccess {
			t.Fatalf("exit = %d, want success", code)
		}
		assertDifferentialRendered(t, res, string(differential.StateAgreement))
	})

	t.Run("disagreement", func(t *testing.T) {
		srv := differentialRPC(t, 0x2, 0) // ledger says revocable; scanner says clear
		res := run(verifySubject(horizon.Flags{}), srv.URL)
		if res.Differential.State != differential.StateDisagreement {
			t.Fatalf("state = %q (%s), want disagreement", res.Differential.State, res.Differential.Reason)
		}
		if res.Differential.Primary == nil || res.Differential.Secondary == nil {
			t.Fatal("disagreement must carry both readings")
		}
		if code := verifyExitCode(res); code != exitMismatch {
			t.Fatalf("exit = %d, want mismatch (%d) even when the hash comparison agreed", code, exitMismatch)
		}
		assertDifferentialRendered(t, res, string(differential.StateDisagreement))
	})

	t.Run("inconclusive_one_side", func(t *testing.T) {
		srv := differentialRPC(t, 0, 0)
		res := run(nil, srv.URL) // the scanner produced no flag reading
		if res.Differential.State != differential.StateInconclusiveOneSide {
			t.Fatalf("state = %q (%s), want inconclusive_one_side", res.Differential.State, res.Differential.Reason)
		}
		if code := verifyExitCode(res); code != exitSuccess {
			t.Fatalf("exit = %d, want the hash verdict's success (inconclusive is not agreement, but it is not a disagreement)", code)
		}
		assertDifferentialRendered(t, res, string(differential.StateInconclusiveOneSide))
	})

	t.Run("inconclusive_both_sides", func(t *testing.T) {
		srv := differentialRPC(t, 0, http.StatusServiceUnavailable)
		res := run(nil, srv.URL)
		if res.Differential.State != differential.StateInconclusiveBothSides {
			t.Fatalf("state = %q (%s), want inconclusive_both_sides", res.Differential.State, res.Differential.Reason)
		}
		if code := verifyExitCode(res); code != exitSuccess {
			t.Fatalf("exit = %d, want the hash verdict's success", code)
		}
		assertDifferentialRendered(t, res, string(differential.StateInconclusiveBothSides))
	})
}

// assertDifferentialRendered checks both output forms name the state, so a
// script and a human see the same four answers.
func assertDifferentialRendered(t *testing.T, res *VerifyResult, state string) {
	t.Helper()
	var text bytes.Buffer
	if err := writeVerifyResult(&text, res, false); err != nil {
		t.Fatalf("write text result: %v", err)
	}
	if !strings.Contains(text.String(), "differential: "+state) {
		t.Fatalf("text output does not render state %q:\n%s", state, text.String())
	}

	var encoded bytes.Buffer
	if err := writeVerifyResult(&encoded, res, true); err != nil {
		t.Fatalf("write JSON result: %v", err)
	}
	var got struct {
		Differential *differential.Result `json:"differential"`
	}
	if err := json.Unmarshal(encoded.Bytes(), &got); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if got.Differential == nil || string(got.Differential.State) != state {
		t.Fatalf("JSON differential = %+v, want state %q", got.Differential, state)
	}
}

// TestVerifyWithoutDifferentialStaysSilent guards the opt-in property: a
// normal verify has no differential field, so existing consumers see exactly
// the shape they saw before.
func TestVerifyWithoutDifferentialStaysSilent(t *testing.T) {
	asset := mechanics.Asset{Code: verifyTestAssetCode, Issuer: verifyTestAssetIssuer}
	rep, params := attestableReport(t, nil)
	chain := chainFrom(params, verifyTableTime.Unix(), false)
	res, err := verifyAsset(context.Background(), asset, 0, verifyTableTime,
		func(context.Context, mechanics.Asset) (*mechanics.Report, *mechanics.Subject, error) {
			return rep, verifySubject(horizon.Flags{}), nil
		},
		func(mechanics.Asset) (*OnChainSafety, error) { return chain, nil }, "")
	if err != nil {
		t.Fatalf("verifyAsset: %v", err)
	}
	if res.Differential != nil {
		t.Fatalf("differential = %+v without -differential", res.Differential)
	}

	var encoded bytes.Buffer
	if err := writeVerifyResult(&encoded, res, true); err != nil {
		t.Fatalf("write JSON result: %v", err)
	}
	if strings.Contains(encoded.String(), "differential") {
		t.Fatalf("JSON mentions differential without the opt-in flag: %s", encoded.String())
	}
}
