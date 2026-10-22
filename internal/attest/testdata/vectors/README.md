# Preimage vectors

Each `<name>.preimage` file is the exact canonical byte sequence that
`evidence_hash` commits to for one report, and `<name>.digest` is its SHA-256
hex digest. A third-party reimplementation of the encoding (any language) can
verify itself against these fixtures without running this repo's code.

`aqua-mainnet-onchain` is traceable to a live mainnet attestation. Its on-chain
`evidence_hash` is
`688453bd22e9b694b9c70659d37526bdae18944645542642008e9d961461a4a9`, recorded
by transaction
`1b6bafc1226570b2415299f5531256716f4d8dc489a9784fcd6ea347d0f63f5f` for asset
`AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA`. The expected
digest, transaction, and deployment table asset are pinned in `vectors.json`.

The vectors were produced by an independent implementation of the documented
rules (`docs/contract-interface.md`), not by the Go encoder — see
`vectors_test.go`, which re-derives every vector from a Report and refuses any
vector file that is not pinned there.
