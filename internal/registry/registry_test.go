package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// Contract ID decoding
// ---------------------------------------------------------------------------

func TestDecodeContractID(t *testing.T) {
	// The live testnet registry contract.
	raw, err := decodeContractID(DefaultContractID)
	if err != nil {
		t.Fatalf("decodeContractID(%q): %v", DefaultContractID, err)
	}
	if len(raw) != 32 {
		t.Fatalf("got %d bytes, want 32", len(raw))
	}
}

func TestDecodeContractID_BadLength(t *testing.T) {
	_, err := decodeContractID("CBAD")
	if !errors.Is(err, ErrContractID) {
		t.Fatalf("expected ErrContractID, got %v", err)
	}
}

func TestDecodeContractID_NotContract(t *testing.T) {
	// A G... account strkey is not a contract.
	_, err := decodeContractID("GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA")
	if !errors.Is(err, ErrContractID) {
		t.Fatalf("expected ErrContractID, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// XDR encoding
// ---------------------------------------------------------------------------

func TestBuildInvokeContractXDR(t *testing.T) {
	contractBytes, err := decodeContractID(DefaultContractID)
	if err != nil {
		t.Fatal(err)
	}
	xdr := buildInvokeContractXDR(contractBytes, "get_safety", []scVal{scAddress(DefaultContractID)})
	if len(xdr) == 0 {
		t.Fatal("empty XDR")
	}
	// Verify the first 4 bytes are the HOST_FUNCTION_TYPE_INVOKE_CONTRACT discriminant (0).
	if readU32(xdr, 0) != 0 {
		t.Fatal("expected HOST_FUNCTION_TYPE_INVOKE_CONTRACT discriminant 0")
	}
}

func TestBuildTransactionEnvelope(t *testing.T) {
	contractBytes, err := decodeContractID(DefaultContractID)
	if err != nil {
		t.Fatal(err)
	}
	invXDR := buildInvokeContractXDR(contractBytes, "is_safe", []scVal{
		scAddress(DefaultContractID),
		scU32(2),
		scU64(3600),
	})
	envelope := buildTransactionEnvelope(invXDR)
	if len(envelope) == 0 {
		t.Fatal("empty envelope")
	}
	// First 4 bytes should be ENVELOPE_TYPE_TX = 2.
	if readU32(envelope, 0) != 2 {
		t.Fatal("expected ENVELOPE_TYPE_TX discriminant 2")
	}
}

// ---------------------------------------------------------------------------
// Recorded RPC fixtures
// ---------------------------------------------------------------------------

// fixtureServer returns an httptest.Server that replays the given body for
// every POST request.
func fixtureServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
}

// A simulated get_safety response for a None result (unattested asset).
const fixtureNoneResult = `{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "results": [{"xdr": "AAAAAQ=="}],
    "latestLedger": 123456
  }
}`

// SCV_VOID is type 1 = [0,0,0,1] = base64 "AAAAAQ==".
func TestGetSafety_None(t *testing.T) {
	srv := fixtureServer(t, fixtureNoneResult)
	defer srv.Close()

	c := New(srv.URL, DefaultContractID)
	safety, err := c.GetSafety(context.Background(), DefaultContractID)
	if !errors.Is(err, ErrNotAttested) {
		t.Fatalf("expected ErrNotAttested, got err=%v safety=%v", err, safety)
	}
	if safety != nil {
		t.Fatal("expected nil safety for None")
	}
}

// A simulated is_safe response returning true.
// SCV_BOOL true = [0,0,0,0, 0,0,0,1] = base64 "AAAAAAAAAAE=".
const fixtureBoolTrue = `{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "results": [{"xdr": "AAAAAAAAAAE="}],
    "latestLedger": 123456
  }
}`

func TestIsSafe_True(t *testing.T) {
	srv := fixtureServer(t, fixtureBoolTrue)
	defer srv.Close()

	c := New(srv.URL, DefaultContractID)
	safe, err := c.IsSafe(context.Background(), DefaultContractID, 2, 3600)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !safe {
		t.Fatal("expected true")
	}
}

// A simulated is_safe response returning false.
// SCV_BOOL false = [0,0,0,0, 0,0,0,0] = base64 "AAAAAAAAAAA=".
const fixtureBoolFalse = `{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "results": [{"xdr": "AAAAAAAAAAA="}],
    "latestLedger": 123456
  }
}`

func TestIsSafe_False(t *testing.T) {
	srv := fixtureServer(t, fixtureBoolFalse)
	defer srv.Close()

	c := New(srv.URL, DefaultContractID)
	safe, err := c.IsSafe(context.Background(), DefaultContractID, 2, 3600)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if safe {
		t.Fatal("expected false")
	}
}

func TestIsSafeMasked_True(t *testing.T) {
	srv := fixtureServer(t, fixtureBoolTrue)
	defer srv.Close()

	c := New(srv.URL, DefaultContractID)
	safe, err := c.IsSafeMasked(context.Background(), DefaultContractID, 6, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !safe {
		t.Fatal("expected true")
	}
}

// ---------------------------------------------------------------------------
// RPC error handling
// ---------------------------------------------------------------------------

const fixtureRPCError = `{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": -32600,
    "message": "Invalid Request"
  }
}`

func TestGetSafety_RPCError(t *testing.T) {
	srv := fixtureServer(t, fixtureRPCError)
	defer srv.Close()

	c := New(srv.URL, DefaultContractID)
	_, err := c.GetSafety(context.Background(), DefaultContractID)
	if !errors.Is(err, ErrRPC) {
		t.Fatalf("expected ErrRPC, got %v", err)
	}
}

func TestGetSafety_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, DefaultContractID)
	_, err := c.GetSafety(context.Background(), DefaultContractID)
	if !errors.Is(err, ErrRPC) {
		t.Fatalf("expected ErrRPC, got %v", err)
	}
}

const fixtureEmptyResults = `{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "results": [],
    "latestLedger": 123456
  }
}`

func TestGetSafety_EmptyResults(t *testing.T) {
	srv := fixtureServer(t, fixtureEmptyResults)
	defer srv.Close()

	c := New(srv.URL, DefaultContractID)
	_, err := c.GetSafety(context.Background(), DefaultContractID)
	if !errors.Is(err, ErrRPC) {
		t.Fatalf("expected ErrRPC, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ScVal encoding round-trips
// ---------------------------------------------------------------------------

func TestScU32(t *testing.T) {
	v := scU32(42)
	encoded := v.encode()
	if len(encoded) != 8 { // 4 bytes type + 4 bytes value
		t.Fatalf("got %d bytes, want 8", len(encoded))
	}
	if readU32(encoded, 0) != scvU32 {
		t.Fatal("type mismatch")
	}
	if readU32(encoded, 4) != 42 {
		t.Fatal("value mismatch")
	}
}

func TestScU64(t *testing.T) {
	v := scU64(86400)
	encoded := v.encode()
	if len(encoded) != 12 { // 4 bytes type + 8 bytes value
		t.Fatalf("got %d bytes, want 12", len(encoded))
	}
	if readU32(encoded, 0) != scvU64 {
		t.Fatal("type mismatch")
	}
	if readU64(encoded, 4) != 86400 {
		t.Fatal("value mismatch")
	}
}

func TestScSymbol(t *testing.T) {
	v := scSymbol("get_safety")
	encoded := v.encode()
	if readU32(encoded, 0) != scvSymbol {
		t.Fatal("type mismatch")
	}
	// Length of "get_safety" is 10.
	if readU32(encoded, 4) != 10 {
		t.Fatalf("symbol length mismatch: got %d", readU32(encoded, 4))
	}
}

// ---------------------------------------------------------------------------
// parseBool edge cases
// ---------------------------------------------------------------------------

func TestParseBool_InvalidType(t *testing.T) {
	// Encode a VOID instead of BOOL.
	xdr := appendU32(nil, scvVoid)
	xdr = appendU32(xdr, 0) // pad to 8 bytes
	raw := json.RawMessage(`"` + encodeBase64(xdr) + `"`)
	_, err := parseBool(raw)
	if !errors.Is(err, ErrDecode) {
		t.Fatalf("expected ErrDecode, got %v", err)
	}
}

func encodeBase64(b []byte) string {
	return encBase64(b)
}

var encBase64 = func(b []byte) string {
	return encBase64Std(b)
}

func encBase64Std(b []byte) string {
	buf := make([]byte, ((len(b)+2)/3)*4)
	n := encBase64Raw(buf, b)
	return string(buf[:n])
}

func encBase64Raw(dst, src []byte) int {
	const encode = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	di, si := 0, 0
	n := (len(src) / 3) * 3
	for si < n {
		val := uint(src[si])<<16 | uint(src[si+1])<<8 | uint(src[si+2])
		dst[di+0] = encode[val>>18&0x3F]
		dst[di+1] = encode[val>>12&0x3F]
		dst[di+2] = encode[val>>6&0x3F]
		dst[di+3] = encode[val&0x3F]
		di += 4
		si += 3
	}
	remain := len(src) - si
	if remain == 0 {
		return di
	}
	val := uint(src[si]) << 16
	if remain == 2 {
		val |= uint(src[si+1]) << 8
	}
	dst[di+0] = encode[val>>18&0x3F]
	dst[di+1] = encode[val>>12&0x3F]
	if remain == 2 {
		dst[di+2] = encode[val>>6&0x3F]
		dst[di+3] = '='
		di += 4
	} else {
		dst[di+2] = '='
		dst[di+3] = '='
		di += 4
	}
	return di
}
