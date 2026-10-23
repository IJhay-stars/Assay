# Frontend TypeScript Example: Reading the Assay Registry

This example demonstrates how to read the Assay safety registry from a front-end application (browser or Node.js dApp backend) using `@stellar/stellar-sdk`.

## Overview

The example simulates read-only contract calls (`get_safety` and `is_safe`) against Soroban RPC on testnet.

- **Zero network transaction fees**: Simulating read-only invocations costs nothing.
- **No secret keys or signing**: It never signs anything and requires no user wallet connection.
- **Fail-closed handling**: Handles `None` (returned as `scvVoid` from `get_safety`) as refusal (`null`), indicating that the asset has no standing attestation.

## Installation & Running

```bash
cd examples/typescript-frontend
npm install
npm run build
npm test
npm run start
```

## Usage

```typescript
import { RegistryReader } from './reader.js';

const reader = new RegistryReader();

// 1. Read the full Safety record for an asset (e.g. AQUA SAC on testnet)
const assetAddress = 'CDJF2JQINO7WRFXB2AAHLONFDPPI4M3W2UM5THGQQ7JMJDIEJYC4CMPG';
const safety = await reader.getSafety(assetAddress);

if (safety === null) {
  // Unattested or revoked asset -> refuse deposit
  console.log('Asset is not attested in the registry!');
} else {
  console.log('Attestation severity:', safety.severity);
  console.log('Mechanic flags:', safety.flags);
  console.log('Evidence hash:', safety.evidence_hash);
  console.log('Attested at:', new Date(Number(safety.attested_at) * 1000));
}

// 2. Perform a policy check (severity <= 2, age <= 1 day)
const isAdmissible = await reader.isSafe(assetAddress, 2, 86400);
if (!isAdmissible) {
  console.log('Asset fails admission policy');
}
```
