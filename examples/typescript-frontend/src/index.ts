import { RegistryReader, DEFAULT_REGISTRY_CONTRACT } from './reader.js';

// An attested SAC address on Stellar testnet (or custom)
// Example testnet asset: AQUA SAC address
const TEST_ASSET = 'CDJF2JQINO7WRFXB2AAHLONFDPPI4M3W2UM5THGQQ7JMJDIEJYC4CMPG';
// A random/unattested contract address to demonstrate handling None
const UNATTESTED_ASSET = 'CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM';

async function main() {
  console.log('Assay Frontend Safety Reader Example');
  console.log('------------------------------------');
  console.log(`Registry Contract: ${DEFAULT_REGISTRY_CONTRACT}`);
  console.log();

  const reader = new RegistryReader();

  console.log(`1. Reading safety for attested asset: ${TEST_ASSET}`);
  try {
    const safety = await reader.getSafety(TEST_ASSET);
    if (safety) {
      console.log('Attestation found:');
      console.log(`  Severity:      ${safety.severity}`);
      console.log(`  Flags:         0b${safety.flags.toString(2)} (${safety.flags})`);
      console.log(`  Evidence Hash: ${safety.evidence_hash}`);
      console.log(`  Attested At:   ${new Date(Number(safety.attested_at) * 1000).toISOString()}`);
    } else {
      console.log('  Result: None (unattested / refused)');
    }

    const isSafe = await reader.isSafe(TEST_ASSET, 2, 86400 * 365);
    console.log(`  isSafe(max_severity=2, max_age=1y): ${isSafe}`);
  } catch (err) {
    console.warn(`  Network read error (offline or testnet reset): ${(err as Error).message}`);
  }

  console.log();
  console.log(`2. Reading safety for unattested asset: ${UNATTESTED_ASSET}`);
  try {
    const safety = await reader.getSafety(UNATTESTED_ASSET);
    if (safety === null) {
      console.log('  Result: null (correctly handled None as refusal)');
    } else {
      console.log('  Unexpectedly found attestation:', safety);
    }
  } catch (err) {
    console.warn(`  Network read error: ${(err as Error).message}`);
  }
}

main().catch((err) => {
  console.error('Fatal error:', err);
  process.exit(1);
});
