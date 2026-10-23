import {
  Account,
  Address,
  Contract,
  Networks,
  TransactionBuilder,
  nativeToScVal,
  rpc,
  scValToNative,
  xdr,
} from '@stellar/stellar-sdk';

export const DEFAULT_REGISTRY_CONTRACT =
  'CBK4FBIHMDTXCUPE4E3ZDVSFJSCY5FJETTKNIQPN4LFJIKKIBLKIXQ73';
export const DEFAULT_RPC_URL = 'https://soroban-testnet.stellar.org';
export const DEFAULT_NETWORK_PASSPHRASE = Networks.TESTNET;

// A dummy account used solely to assemble an unsigned Transaction envelope
// for simulation. Simulation never verifies account sequence or signature.
const DUMMY_SOURCE_ACCOUNT =
  'GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA';

export interface Safety {
  severity: number;
  flags: number;
  evidence_hash: string;
  attested_at: bigint;
}

export interface RegistryReaderOptions {
  rpcUrl?: string;
  contractId?: string;
  networkPassphrase?: string;
}

/**
 * RegistryReader reads safety attestations from the on-chain Assay safety registry
 * by simulating read-only calls against Soroban RPC.
 *
 * It is completely read-only, never requires secret keys or signatures, and incurs
 * zero network transaction fees.
 */
export class RegistryReader {
  private server: rpc.Server;
  private contractId: string;
  private networkPassphrase: string;
  private contract: Contract;

  constructor(options: RegistryReaderOptions = {}) {
    const rpcUrl = options.rpcUrl || DEFAULT_RPC_URL;
    this.contractId = options.contractId || DEFAULT_REGISTRY_CONTRACT;
    this.networkPassphrase = options.networkPassphrase || DEFAULT_NETWORK_PASSPHRASE;
    this.server = new rpc.Server(rpcUrl, {
      allowHttp: rpcUrl.startsWith('http://localhost') || rpcUrl.startsWith('http://127.0.0.1'),
    });
    this.contract = new Contract(this.contractId);
  }

  /**
   * Reads get_safety(asset) from the registry.
   * Returns Safety object if attested, or null if the asset is not attested (None).
   *
   * @param assetAddress The Stellar Contract address (C...) or SAC address of the asset.
   */
  async getSafety(assetAddress: string): Promise<Safety | null> {
    const op = this.contract.call('get_safety', new Address(assetAddress).toScVal());
    const retval = await this.simulateOperation(op);
    return parseSafetyOption(retval);
  }

  /**
   * Calls is_safe(asset, max_severity, max_age_secs) via simulation.
   * Returns false if unattested, stale, or exceeding max_severity.
   *
   * @param assetAddress The Stellar Contract address (C...) or SAC address of the asset.
   * @param maxSeverity The maximum accepted severity level (e.g. 0 to 4).
   * @param maxAgeSecs The maximum age in seconds for freshness check (0 disables).
   */
  async isSafe(
    assetAddress: string,
    maxSeverity: number,
    maxAgeSecs: number | bigint
  ): Promise<boolean> {
    const op = this.contract.call(
      'is_safe',
      new Address(assetAddress).toScVal(),
      nativeToScVal(maxSeverity, { type: 'u32' }),
      nativeToScVal(BigInt(maxAgeSecs), { type: 'u64' })
    );
    const retval = await this.simulateOperation(op);
    return Boolean(scValToNative(retval));
  }

  /**
   * Builds an unsigned transaction and simulates it against Soroban RPC.
   */
  private async simulateOperation(op: xdr.Operation): Promise<xdr.ScVal> {
    const account = new Account(DUMMY_SOURCE_ACCOUNT, '0');
    const tx = new TransactionBuilder(account, {
      fee: '100',
      networkPassphrase: this.networkPassphrase,
    })
      .addOperation(op)
      .setTimeout(30)
      .build();

    const simRes = await this.server.simulateTransaction(tx);
    if (rpc.Api.isSimulationError(simRes)) {
      throw new Error(`Simulation failed: ${simRes.error}`);
    }

    if (!simRes.result || !simRes.result.retval) {
      throw new Error('Simulation returned no result');
    }

    return simRes.result.retval;
  }
}

/**
 * Parses an ScVal representing Option<Safety>.
 * Returns null if the ScVal represents None (scvVoid), or the decoded Safety record.
 */
export function parseSafetyOption(val: xdr.ScVal): Safety | null {
  if (val.type === 'scvVoid') {
    return null;
  }

  const native = scValToNative(val);
  if (!native || typeof native !== 'object') {
    return null;
  }

  let evidenceHash = '';
  if (native.evidence_hash) {
    if (typeof native.evidence_hash === 'string') {
      evidenceHash = native.evidence_hash;
    } else if (Buffer.isBuffer(native.evidence_hash) || native.evidence_hash instanceof Uint8Array) {
      evidenceHash = Buffer.from(native.evidence_hash).toString('hex');
    }
  }

  return {
    severity: Number(native.severity ?? 0),
    flags: Number(native.flags ?? 0),
    evidence_hash: evidenceHash,
    attested_at: BigInt(native.attested_at ?? 0),
  };
}
