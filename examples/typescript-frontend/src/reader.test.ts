import { describe, it } from 'node:test';
import assert from 'node:assert';
import { xdr, nativeToScVal } from '@stellar/stellar-sdk';
import { parseSafetyOption } from './reader.js';

describe('parseSafetyOption', () => {
  it('parses scvVoid as null (unattested / None)', () => {
    const voidVal = xdr.ScVal.scvVoid();
    const result = parseSafetyOption(voidVal);
    assert.strictEqual(result, null);
  });

  it('parses a valid Safety struct map', () => {
    const rawHash = Buffer.from('abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789', 'hex');
    const safetyMap = {
      severity: 1,
      flags: 2,
      evidence_hash: rawHash,
      attested_at: 1727352000n,
    };

    const scVal = nativeToScVal(safetyMap);
    const parsed = parseSafetyOption(scVal);

    assert.notStrictEqual(parsed, null);
    assert.strictEqual(parsed!.severity, 1);
    assert.strictEqual(parsed!.flags, 2);
    assert.strictEqual(parsed!.evidence_hash, 'abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789');
    assert.strictEqual(parsed!.attested_at, 1727352000n);
  });
});
