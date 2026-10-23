// Package sac derives the Stellar Asset Contract address of a classic asset.
//
// The registry keys every attestation on the asset's SAC address, and that
// address is network-scoped: the same CODE-ISSUER has a different contract on
// pubnet and on testnet. This package computes exactly what
// `stellar contract id asset --asset CODE:ISSUER --network NETWORK` prints, so
// the attester can derive it for the registry's network without shelling out.
//
// The derivation is the protocol's, not a convention:
//
//	contract_id = SHA-256( XDR HashIDPreimage {
//	    type:      ENVELOPE_TYPE_CONTRACT_ID,
//	    networkID: SHA-256(network passphrase),
//	    contractIDPreimage: { type: CONTRACT_ID_PREIMAGE_FROM_ASSET, fromAsset: Asset },
//	})
//
// rendered as a `C…` strkey. No I/O: the network is an argument.
package sac

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// XDR discriminants, fixed by the protocol (Stellar-transaction.x,
// Stellar-ledger-entries.x, Stellar-types.x).
const (
	envelopeTypeContractID           = 8
	contractIDPreimageFromAsset      = 1
	assetTypeNative                  = 0
	assetTypeCreditAlphanum4         = 1
	assetTypeCreditAlphanum12        = 2
	publicKeyTypeEd25519             = 0
	strkeyVersionAccountID      byte = 6 << 3 // 'G'
	strkeyVersionContract       byte = 2 << 3 // 'C'
)

// ErrInvalidAsset reports an asset code or issuer that cannot be encoded.
var ErrInvalidAsset = errors.New("sac: invalid asset")

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// Native returns the SAC address of the network's native asset (XLM).
func Native(networkPassphrase string) string {
	asset := make([]byte, 4)
	binary.BigEndian.PutUint32(asset, assetTypeNative)
	return contractAddress(networkPassphrase, asset)
}

// Address returns the SAC address of the classic asset code/issuer on the
// network identified by networkPassphrase.
func Address(code, issuer, networkPassphrase string) (string, error) {
	asset, err := encodeAsset(code, issuer)
	if err != nil {
		return "", err
	}
	return contractAddress(networkPassphrase, asset), nil
}

func contractAddress(networkPassphrase string, assetXDR []byte) string {
	networkID := sha256.Sum256([]byte(networkPassphrase))

	preimage := make([]byte, 0, 4+32+4+len(assetXDR))
	preimage = binary.BigEndian.AppendUint32(preimage, envelopeTypeContractID)
	preimage = append(preimage, networkID[:]...)
	preimage = binary.BigEndian.AppendUint32(preimage, contractIDPreimageFromAsset)
	preimage = append(preimage, assetXDR...)

	id := sha256.Sum256(preimage)
	return encodeStrkey(strkeyVersionContract, id[:])
}

// encodeAsset renders the XDR Asset union for a credit asset.
func encodeAsset(code, issuer string) ([]byte, error) {
	if code == "" || len(code) > 12 {
		return nil, fmt.Errorf("%w: code %q must be 1-12 characters", ErrInvalidAsset, code)
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return nil, fmt.Errorf("%w: code %q must be alphanumeric", ErrInvalidAsset, code)
		}
	}
	key, err := decodeStrkey(strkeyVersionAccountID, issuer)
	if err != nil {
		return nil, fmt.Errorf("%w: issuer %q: %w", ErrInvalidAsset, issuer, err)
	}

	assetType, width := uint32(assetTypeCreditAlphanum4), 4
	if len(code) > 4 {
		assetType, width = assetTypeCreditAlphanum12, 12
	}
	out := binary.BigEndian.AppendUint32(nil, assetType)
	padded := make([]byte, width)
	copy(padded, code)
	out = append(out, padded...)
	out = binary.BigEndian.AppendUint32(out, publicKeyTypeEd25519)
	out = append(out, key...)
	return out, nil
}

func encodeStrkey(version byte, payload []byte) string {
	raw := make([]byte, 0, 1+len(payload)+2)
	raw = append(raw, version)
	raw = append(raw, payload...)
	raw = binary.LittleEndian.AppendUint16(raw, crc16XModem(raw))
	return b32.EncodeToString(raw)
}

func decodeStrkey(version byte, s string) ([]byte, error) {
	if len(s) != 56 || s != strings.ToUpper(s) {
		return nil, errors.New("not a 56-character strkey")
	}
	raw, err := b32.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("not base32: %w", err)
	}
	if len(raw) != 35 {
		return nil, errors.New("wrong decoded length")
	}
	if raw[0] != version {
		return nil, errors.New("wrong strkey version")
	}
	body, sum := raw[:33], binary.LittleEndian.Uint16(raw[33:])
	if crc16XModem(body) != sum {
		return nil, errors.New("checksum mismatch")
	}
	return body[1:], nil
}

// IsContractAddress reports whether s is a well-formed contract strkey (C…).
func IsContractAddress(s string) bool {
	_, err := decodeStrkey(strkeyVersionContract, s)
	return err == nil
}

// crc16XModem is the checksum strkey appends (polynomial 0x1021, init 0).
func crc16XModem(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
