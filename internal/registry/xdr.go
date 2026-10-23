package registry

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// scVal represents a Soroban ScVal for building invocation arguments.
type scVal struct {
	typ  uint32 // ScVal discriminant
	data []byte // type-specific payload
}

// ScVal type discriminants from the Stellar XDR.
const (
	scvBool    uint32 = 0  // SCV_BOOL
	scvVoid    uint32 = 1  // SCV_VOID
	scvU32     uint32 = 5  // SCV_U32
	scvI32     uint32 = 6  // SCV_I32
	scvU64     uint32 = 7  // SCV_U64
	scvI64     uint32 = 8  // SCV_I64
	scvSymbol  uint32 = 14 // SCV_SYMBOL
	scvAddress uint32 = 18 // SCV_ADDRESS
	scvMap     uint32 = 20 // SCV_MAP
	scvVec     uint32 = 16 // SCV_VEC
	scvBytes   uint32 = 13 // SCV_BYTES
)

// scAddress builds an ScVal of type SCV_ADDRESS for a contract address.
func scAddress(contractID string) scVal {
	raw, _ := decodeContractID(contractID)
	// ScAddress = [1: SC_ADDRESS_TYPE_CONTRACT] Hash(32)
	var data []byte
	data = appendU32(data, 1) // SC_ADDRESS_TYPE_CONTRACT
	data = append(data, raw...)
	return scVal{typ: scvAddress, data: data}
}

// scU32 builds an ScVal of type SCV_U32.
func scU32(v uint32) scVal {
	return scVal{typ: scvU32, data: appendU32(nil, v)}
}

// scU64 builds an ScVal of type SCV_U64.
func scU64(v uint64) scVal {
	return scVal{typ: scvU64, data: appendU64(nil, v)}
}

// scSymbol builds an ScVal of type SCV_SYMBOL.
func scSymbol(s string) scVal {
	var data []byte
	data = appendU32(data, uint32(len(s)))
	data = append(data, s...)
	// XDR strings are padded to a multiple of 4 bytes.
	for len(data)%4 != 0 {
		data = append(data, 0)
	}
	return scVal{typ: scvSymbol, data: data}
}

// encode serializes the ScVal to XDR bytes.
func (v scVal) encode() []byte {
	buf := appendU32(nil, v.typ)
	buf = append(buf, v.data...)
	return buf
}

// buildInvokeContractXDR builds the HostFunction XDR for invoking a contract
// function. The layout:
//
//	HostFunction = [0: HOST_FUNCTION_TYPE_INVOKE_CONTRACT] InvokeContractArgs
//	InvokeContractArgs = ScAddress functionName ScVal[]
func buildInvokeContractXDR(contractBytes []byte, fnName string, args []scVal) []byte {
	var buf []byte
	// HostFunction discriminant: HOST_FUNCTION_TYPE_INVOKE_CONTRACT = 0
	buf = appendU32(buf, 0)

	// InvokeContractArgs:
	// 1. contractAddress as ScAddress (ScVal type 18, address type 1)
	buf = appendU32(buf, 1) // SC_ADDRESS_TYPE_CONTRACT
	buf = append(buf, contractBytes...)

	// 2. functionName as ScSymbol (just the variable-length string)
	buf = appendU32(buf, uint32(len(fnName)))
	buf = append(buf, fnName...)
	for len(buf)%4 != 0 {
		buf = append(buf, 0)
	}

	// 3. args as ScVal[]
	buf = appendU32(buf, uint32(len(args)))
	for _, arg := range args {
		buf = append(buf, arg.encode()...)
	}

	return buf
}

// buildTransactionEnvelope wraps a host function invocation in a minimal
// transaction envelope suitable for simulateTransaction. The simulation
// endpoint does not verify signatures or sequence numbers, so the envelope
// can carry dummy values — only the invocation matters.
func buildTransactionEnvelope(hostFnXDR []byte) []byte {
	var buf []byte

	// TransactionEnvelope discriminant: ENVELOPE_TYPE_TX = 2
	buf = appendU32(buf, 2)

	// Transaction:
	// sourceAccount: MuxedAccount [0: KEY_TYPE_ED25519] + 32 zero bytes
	buf = appendU32(buf, 0)
	buf = append(buf, make([]byte, 32)...)

	// fee: uint32
	buf = appendU32(buf, 100)

	// seqNum: SequenceNumber (int64)
	buf = appendU64(buf, 0)

	// timeBounds: optional (none)
	// Cond discriminant: PRECOND_NONE = 0
	buf = appendU32(buf, 0)

	// memo: MEMO_NONE = 0
	buf = appendU32(buf, 0)

	// operations: Vec<Operation> length 1
	buf = appendU32(buf, 1)

	// Operation:
	// sourceAccount: optional (none)
	buf = appendU32(buf, 0)

	// body discriminant: INVOKE_HOST_FUNCTION = 24
	buf = appendU32(buf, 24)

	// InvokeHostFunctionOp:
	// hostFunction: the invocation XDR
	buf = append(buf, hostFnXDR...)

	// auth: Vec<SorobanAuthorizationEntry> length 0
	buf = appendU32(buf, 0)

	// ext: union v0
	buf = appendU32(buf, 0)

	// DecoratedSignature[]: length 0 (simulation doesn't check signatures)
	buf = appendU32(buf, 0)

	return buf
}

// parseSafetyOption decodes the simulation result into a *Safety (nil for None).
// The result XDR is an ScVal encoding of Option<Safety>.
func parseSafetyOption(resultRaw json.RawMessage) (*Safety, error) {
	var xdrB64 string
	if err := json.Unmarshal(resultRaw, &xdrB64); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDecode, err)
	}

	raw, err := base64.StdEncoding.DecodeString(xdrB64)
	if err != nil {
		return nil, fmt.Errorf("%w: base64: %w", ErrDecode, err)
	}

	if len(raw) < 4 {
		return nil, fmt.Errorf("%w: result too short", ErrDecode)
	}

	// The simulation result is wrapped in an ScVal.
	// Option<Safety> is encoded as SCV_VOID for None, or SCV_MAP for Some.
	typ := readU32(raw, 0)

	if typ == scvVoid {
		return nil, ErrNotAttested
	}

	if typ != scvMap {
		return nil, fmt.Errorf("%w: expected MAP or VOID, got type %d", ErrDecode, typ)
	}

	return parseMapSafety(raw[4:])
}

// parseMapSafety parses a Safety struct encoded as an ScVal Map.
// Map layout: optional-present (4 bytes) + len (4 bytes) + entries
func parseMapSafety(data []byte) (*Safety, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("%w: map too short", ErrDecode)
	}
	// Optional presence marker for the map
	present := readU32(data, 0)
	off := 4
	if present == 0 {
		// None variant
		return nil, ErrNotAttested
	}

	if len(data) < off+4 {
		return nil, fmt.Errorf("%w: map length missing", ErrDecode)
	}
	mapLen := readU32(data, off)
	off += 4

	s := &Safety{}
	for i := uint32(0); i < mapLen; i++ {
		if len(data) < off+4 {
			return nil, fmt.Errorf("%w: map entry truncated at key type", ErrDecode)
		}
		keyType := readU32(data, off)
		off += 4

		var keyName string
		if keyType == scvSymbol {
			if len(data) < off+4 {
				return nil, fmt.Errorf("%w: symbol length missing", ErrDecode)
			}
			sLen := readU32(data, off)
			off += 4
			if len(data) < off+int(sLen) {
				return nil, fmt.Errorf("%w: symbol data truncated", ErrDecode)
			}
			keyName = string(data[off : off+int(sLen)])
			off += int(sLen)
			// Pad to 4-byte boundary
			for off%4 != 0 {
				off++
			}
		} else {
			return nil, fmt.Errorf("%w: expected symbol key, got type %d", ErrDecode, keyType)
		}

		// Read value
		if len(data) < off+4 {
			return nil, fmt.Errorf("%w: value type missing for key %s", ErrDecode, keyName)
		}
		valType := readU32(data, off)
		off += 4

		switch keyName {
		case "severity":
			if valType != scvU32 {
				return nil, fmt.Errorf("%w: severity not u32", ErrDecode)
			}
			if len(data) < off+4 {
				return nil, fmt.Errorf("%w: severity value truncated", ErrDecode)
			}
			s.Severity = readU32(data, off)
			off += 4
		case "flags":
			if valType != scvU32 {
				return nil, fmt.Errorf("%w: flags not u32", ErrDecode)
			}
			if len(data) < off+4 {
				return nil, fmt.Errorf("%w: flags value truncated", ErrDecode)
			}
			s.Flags = readU32(data, off)
			off += 4
		case "evidence_hash":
			if valType != scvBytes {
				return nil, fmt.Errorf("%w: evidence_hash not bytes", ErrDecode)
			}
			if len(data) < off+4 {
				return nil, fmt.Errorf("%w: evidence_hash length missing", ErrDecode)
			}
			bLen := readU32(data, off)
			off += 4
			if len(data) < off+int(bLen) {
				return nil, fmt.Errorf("%w: evidence_hash truncated", ErrDecode)
			}
			s.EvidenceHash = hex.EncodeToString(data[off : off+int(bLen)])
			off += int(bLen)
			for off%4 != 0 {
				off++
			}
		case "attested_at":
			if valType != scvU64 {
				return nil, fmt.Errorf("%w: attested_at not u64", ErrDecode)
			}
			if len(data) < off+8 {
				return nil, fmt.Errorf("%w: attested_at value truncated", ErrDecode)
			}
			s.AttestedAt = readU64(data, off)
			off += 8
		default:
			// Skip unknown keys — forward compatibility.
			off, err := skipScVal(data, off-4)
			if err != nil {
				return nil, err
			}
			_ = off
		}
	}

	return s, nil
}

// parseBool decodes the simulation result into a bool.
func parseBool(resultRaw json.RawMessage) (bool, error) {
	var xdrB64 string
	if err := json.Unmarshal(resultRaw, &xdrB64); err != nil {
		return false, fmt.Errorf("%w: %w", ErrDecode, err)
	}

	raw, err := base64.StdEncoding.DecodeString(xdrB64)
	if err != nil {
		return false, fmt.Errorf("%w: base64: %w", ErrDecode, err)
	}

	if len(raw) < 8 {
		return false, fmt.Errorf("%w: bool result too short", ErrDecode)
	}

	typ := readU32(raw, 0)
	if typ != scvBool {
		return false, fmt.Errorf("%w: expected BOOL, got type %d", ErrDecode, typ)
	}

	val := readU32(raw, 4)
	return val != 0, nil
}

// skipScVal skips over an ScVal at the given offset (starting at the type
// discriminant) and returns the offset past the value.
func skipScVal(data []byte, off int) (int, error) {
	if len(data) < off+4 {
		return off, fmt.Errorf("%w: cannot skip scval", ErrDecode)
	}
	typ := readU32(data, off)
	off += 4
	switch typ {
	case scvBool:
		return off + 4, nil
	case scvVoid:
		return off, nil
	case scvU32, scvI32:
		return off + 4, nil
	case scvU64, scvI64:
		return off + 8, nil
	case scvSymbol, scvBytes:
		if len(data) < off+4 {
			return off, fmt.Errorf("%w: skip: length missing", ErrDecode)
		}
		sLen := readU32(data, off)
		off += 4 + int(sLen)
		for off%4 != 0 {
			off++
		}
		return off, nil
	default:
		return off, fmt.Errorf("%w: skip: unsupported type %d", ErrDecode, typ)
	}
}

// appendU32 appends a big-endian uint32 to buf.
func appendU32(buf []byte, v uint32) []byte {
	return append(buf, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// appendU64 appends a big-endian uint64 to buf.
func appendU64(buf []byte, v uint64) []byte {
	return append(buf,
		byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32),
		byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// readU32 reads a big-endian uint32 at offset off.
func readU32(b []byte, off int) uint32 {
	return uint32(b[off])<<24 | uint32(b[off+1])<<16 | uint32(b[off+2])<<8 | uint32(b[off+3])
}

// readU64 reads a big-endian uint64 at offset off.
func readU64(b []byte, off int) uint64 {
	return uint64(b[off])<<56 | uint64(b[off+1])<<48 | uint64(b[off+2])<<40 | uint64(b[off+3])<<32 |
		uint64(b[off+4])<<24 | uint64(b[off+5])<<16 | uint64(b[off+6])<<8 | uint64(b[off+7])
}
