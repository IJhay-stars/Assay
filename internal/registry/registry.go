// Package registry reads the Assay safety registry on-chain through Soroban
// RPC simulation, without shelling out to the Stellar CLI.
//
// It exposes three calls — GetSafety, IsSafe, IsSafeMasked — that correspond
// 1-to-1 with the contract's read-only entrypoints. Every call is a simulated
// invocation that costs nothing and needs no signature: the same mechanism
// `make read` uses through the CLI, but without a child process or a PATH
// dependency.
//
// The package hand-encodes the minimal XDR needed to build a
// simulateTransaction request, following the same approach as
// internal/differential. It adds no new go.mod dependency.
package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/use-assay/assay/internal/horizon"
)

// DefaultRPCURL is the SDF-operated Soroban RPC instance for testnet.
const DefaultRPCURL = "https://soroban-testnet.stellar.org"

// DefaultContractID is the live testnet registry deployment.
const DefaultContractID = "CBK4FBIHMDTXCUPE4E3ZDVSFJSCY5FJETTKNIQPN4LFJIKKIBLKIXQ73"

// MaxBody caps an RPC response read.
const MaxBody = 1 << 20

// Safety mirrors the on-chain Safety struct returned by get_safety.
type Safety struct {
	Severity     uint32 `json:"severity"`
	Flags        uint32 `json:"flags"`
	EvidenceHash string `json:"evidence_hash"`
	AttestedAt   uint64 `json:"attested_at"`
}

// Sentinel errors.
var (
	// ErrNotAttested reports that get_safety returned None for this asset.
	ErrNotAttested = errors.New("registry: asset not attested")
	// ErrRPC reports a transport- or protocol-level failure talking to the
	// Soroban RPC endpoint.
	ErrRPC = errors.New("registry: soroban rpc")
	// ErrDecode reports that the RPC result could not be decoded.
	ErrDecode = errors.New("registry: decode result")
	// ErrContractID reports an invalid contract ID.
	ErrContractID = errors.New("registry: invalid contract id")
)

// Client reads from the Assay safety registry via Soroban RPC simulation.
type Client struct {
	RPCURL     string
	ContractID string
	HTTP       *http.Client
	UserAgent  string
}

// New returns a Client for the given Soroban RPC URL and contract ID,
// defaulting to the SDF testnet instance and the live registry.
func New(rpcURL, contractID string) *Client {
	if rpcURL == "" {
		rpcURL = DefaultRPCURL
	}
	if contractID == "" {
		contractID = DefaultContractID
	}
	return &Client{
		RPCURL:     rpcURL,
		ContractID: contractID,
		HTTP:       &http.Client{Timeout: 15 * time.Second},
		UserAgent: "assay/" + horizon.Version +
			" (+https://github.com/use-assay/Assay)",
	}
}

// GetSafety reads the attestation for asset (a SAC address). Returns nil if
// the asset has no attestation (None on-chain).
func (c *Client) GetSafety(ctx context.Context, asset string) (*Safety, error) {
	// Build the invocation XDR for get_safety(asset).
	fnName := "get_safety"
	args := []scVal{scAddress(asset)}
	result, err := c.simulate(ctx, fnName, args)
	if err != nil {
		return nil, err
	}
	return parseSafetyOption(result)
}

// IsSafe calls the registry's is_safe(asset, max_severity, max_age_secs).
func (c *Client) IsSafe(ctx context.Context, asset string, maxSeverity uint32, maxAgeSecs uint64) (bool, error) {
	fnName := "is_safe"
	args := []scVal{
		scAddress(asset),
		scU32(maxSeverity),
		scU64(maxAgeSecs),
	}
	result, err := c.simulate(ctx, fnName, args)
	if err != nil {
		return false, err
	}
	return parseBool(result)
}

// IsSafeMasked calls the registry's is_safe_masked(asset, forbidden_mask, max_age_secs).
func (c *Client) IsSafeMasked(ctx context.Context, asset string, forbiddenMask uint32, maxAgeSecs uint64) (bool, error) {
	fnName := "is_safe_masked"
	args := []scVal{
		scAddress(asset),
		scU32(forbiddenMask),
		scU64(maxAgeSecs),
	}
	result, err := c.simulate(ctx, fnName, args)
	if err != nil {
		return false, err
	}
	return parseBool(result)
}

// simulate sends a simulateTransaction request for a read-only contract call.
// It returns the raw JSON result value from the simulation.
func (c *Client) simulate(ctx context.Context, fnName string, args []scVal) (json.RawMessage, error) {
	contractBytes, err := decodeContractID(c.ContractID)
	if err != nil {
		return nil, err
	}

	// Build the invokeHostFunction XDR.
	invocationXDR := buildInvokeContractXDR(contractBytes, fnName, args)

	// Build a minimal transaction envelope wrapping this invocation.
	txEnvelope := buildTransactionEnvelope(invocationXDR)
	txBase64 := base64.StdEncoding.EncodeToString(txEnvelope)

	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "simulateTransaction",
		"params": map[string]any{
			"transaction": txBase64,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("%w: encode request: %w", ErrRPC, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.RPCURL, strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %w", ErrRPC, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: post %s: %w", ErrRPC, c.RPCURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrRPC, resp.StatusCode)
	}

	var body struct {
		Result *struct {
			Results []struct {
				XDR string `json:"xdr"`
			} `json:"results"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, MaxBody))
	if err := dec.Decode(&body); err != nil {
		return nil, fmt.Errorf("%w: decode response: %w", ErrRPC, err)
	}
	if body.Error != nil {
		return nil, fmt.Errorf("%w: rpc error %d: %s", ErrRPC, body.Error.Code, body.Error.Message)
	}
	if body.Result == nil || len(body.Result.Results) == 0 {
		return nil, fmt.Errorf("%w: no simulation results", ErrRPC)
	}

	resultXDR := body.Result.Results[0].XDR
	return json.RawMessage(`"` + resultXDR + `"`), nil
}

// decodeContractID decodes a Stellar contract ID (C... strkey) to its 32
// raw bytes. Contract IDs use the same base32 encoding as account strkeys
// but with version byte 2 (type 'C').
func decodeContractID(id string) ([]byte, error) {
	if len(id) != 56 {
		return nil, fmt.Errorf("%w: %q is %d characters, want 56", ErrContractID, id, len(id))
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	raw := make([]byte, 0, 35)
	var acc uint32
	var bits uint8
	for i := 0; i < len(id); i++ {
		v := strings.IndexByte(alphabet, id[i])
		if v < 0 {
			return nil, fmt.Errorf("%w: %q has a character outside the base32 alphabet", ErrContractID, id)
		}
		acc = acc<<5 | uint32(v)
		bits += 5
		if bits >= 8 {
			bits -= 8
			raw = append(raw, byte(acc>>bits))
		}
	}
	// Version byte for contract strkey is 2<<3 = 0x10 (type 'C').
	if raw[0] != 2<<3 {
		return nil, fmt.Errorf("%w: %q is not a contract strkey", ErrContractID, id)
	}
	return raw[1:33], nil
}
