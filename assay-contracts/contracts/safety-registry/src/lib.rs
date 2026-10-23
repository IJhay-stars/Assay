#![no_std]
//! Assay safety registry: an on-chain gate for Stellar asset trap mechanics.
//!
//! # What this contract is, and what it is not
//!
//! A Soroban contract cannot call Horizon, fetch a stellar.toml, or read an
//! issuer's authorization flags from within a transaction. So this contract
//! does not scan anything. It stores *attestations* produced by an off-chain
//! Assay scanner and lets another contract read one atomically, in the same
//! transaction as the action it is protecting.
//!
//! That distinction is deliberate and is stated here rather than hidden behind
//! a reassuring function name. A caller is trusting the attester, plus the
//! `evidence_hash` that lets anyone verify the attestation against the evidence
//! the scanner actually fetched.
//!
//! # Why severity is safe to gate on
//!
//! [`Safety::severity`] is capability-only: it is derived from the issuer's
//! authorization flags and never adjusted by reputation, attribution, age, or
//! popularity. Two assets whose issuers hold identical power over holders get
//! identical severity. A gate reading `severity <= MEDIUM` is therefore relying
//! on a statement about ledger mechanics, not on anyone's opinion of an issuer.
//!
//! The one exception moves in the safe direction only: a confirmed malicious
//! listing escalates severity to [`SEVERITY_CRITICAL`]. Reputation can raise a
//! level, never lower one.

use soroban_sdk::{
    contract, contracterror, contractevent, contractimpl, contracttype, Address, BytesN, Env, Vec,
};

/// No authorization flags: the issuer has no special power over holders.
pub const SEVERITY_CLEAR: u32 = 0;
/// `auth_required`: the issuer controls who may open a trustline.
pub const SEVERITY_LOW: u32 = 1;
/// `auth_revocable`: the issuer can freeze an existing holder's balance.
pub const SEVERITY_MEDIUM: u32 = 2;
/// `auth_clawback_enabled`: the issuer can confiscate and burn a balance.
pub const SEVERITY_HIGH: u32 = 3;
/// Reserved for reputation escalation; never produced by reading flags.
pub const SEVERITY_CRITICAL: u32 = 4;

/// Mechanic bits. These positions are ABI and must not be renumbered; they
/// match `internal/mechanics.Mechanic` on the Go side.
pub const MECH_AUTH_REQUIRED: u32 = 1 << 0;
pub const MECH_AUTH_REVOCABLE: u32 = 1 << 1;
pub const MECH_CLAWBACK_ENABLED: u32 = 1 << 2;
pub const MECH_FLAGS_LOCKED: u32 = 1 << 3;
pub const MECH_DOMAIN_UNVERIFIED: u32 = 1 << 4;
pub const MECH_BLOCKLISTED: u32 = 1 << 5;

/// Mechanics that let an issuer take a balance outright. Any asset matching
/// this mask has `severity >= SEVERITY_HIGH` by construction.
pub const CONFISCATION_MASK: u32 = MECH_CLAWBACK_ENABLED;

/// Capability bits only: the mechanics that are issuer powers over a holder's
/// balance (auth_required, auth_revocable, auth_clawback_enabled). Everything
/// outside this mask (auth_immutable, domain_unverified, blocklisted) is a
/// reported fact, not a power.
///
/// This is the mask a consumer should reach for when it wants "the dangerous
/// bits": #26 happened because a caller hand-rolled that mask from memory and
/// silently missed the other half of the bitset. The Go side exports the same
/// value as `mechanics.CapabilityMask`; the ABI drift test fails the build if
/// the two ever disagree. Bit positions are unchanged — existing attestations
/// commit to them.
pub const CAPABILITY_MASK: u32 = MECH_AUTH_REQUIRED | MECH_AUTH_REVOCABLE | MECH_CLAWBACK_ENABLED;

/// Named forbidden-bit masks for [`SafetyRegistry::is_safe_masked`]. They exist
/// so a caller expresses a policy ("I never accept confiscation") rather than
/// hand-rolling bits. A caller may still pass any `u32`; these are the two
/// documented shapes.
///
/// `POLICY_MASK_CONFISCATION_ONLY` refuses only confiscation capability. A
/// protocol that can tolerate a freeze but never a clawback uses this mask.
pub const POLICY_MASK_CONFISCATION_ONLY: u32 = MECH_CLAWBACK_ENABLED;

/// `POLICY_MASK_FREEZE_INCLUSIVE` refuses both freeze and confiscation. A
/// custody product that must never see a holder's balance altered by the
/// issuer uses this mask.
pub const POLICY_MASK_FREEZE_INCLUSIVE: u32 = MECH_AUTH_REVOCABLE | MECH_CLAWBACK_ENABLED;

/// The contract's interface version, bumped on every ABI change.
///
/// Consumers and drift tooling can ask "what is this contract?" by calling
/// [`Self::version`] instead of inferring the interface from a wasm hash.
///
/// Bump this whenever the entrypoint set, the [`Safety`] or [`Attestation`]
/// shapes, the error codes, or the event schema change. Additive read-only
/// entrypoints do not require a bump; removing or renaming one does. Version 1
/// established the interface with [`Self::version`]; version 2 adds
/// [`Self::cancel_admin_transfer`] and the [`AdminTransferCancelled`] event.
pub const INTERFACE_VERSION: u32 = 3;

/// Event emitted on every successful `attest` write. Rejected attestations
/// (`InvalidSeverity`, `InconsistentAttestation`, unauthorized caller) publish
/// nothing, so observers never see writes that did not happen.
///
/// Topics: `("attest", asset)`. The asset address is a topic (not a data
/// field) so indexers can subscribe by asset without decoding every event.
/// Two topics stay well within the SDK's four-topic limit.
///
/// Data is a `Map` keyed by field name for readability from RPC output. The
/// exact shape is part of the observable ABI: see `docs/contract-interface.md`.
#[contractevent(topics = ["attest"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Attested {
    /// The attested asset, identified by its Stellar Asset Contract address.
    #[topic]
    pub asset: Address,
    /// Capability severity written for the asset.
    pub severity: u32,
    /// Mechanic bitset written for the asset.
    pub flags: u32,
    /// Ledger timestamp of the write, obtained from `env.ledger().timestamp()`
    /// (seconds since Unix epoch). Authoritative for all on-chain freshness
    /// decisions (`is_safe`, `is_safe_masked`). See `docs/timestamps.md`.
    pub attested_at: u64,
}

/// Event emitted on every successful `revoke`. A revoke that fails
/// (`NotAttested`, unauthorized caller) publishes nothing.
///
/// Topics: `("revoke", asset)`, mirroring [`Attested`] so an indexer tracking
/// an asset by topic sees both the write and its withdrawal. Without this an
/// indexer that recorded an `attest` event would go on believing the
/// attestation stands after it has been removed from storage.
///
/// This is also the only place revoked and never-attested differ: on-chain,
/// `get_safety` returns `None` for both. See `docs/contract-interface.md`.
#[contractevent(topics = ["revoke"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Revoked {
    /// The asset whose attestation was withdrawn.
    #[topic]
    pub asset: Address,
    /// Ledger timestamp of the revocation.
    pub revoked_at: u64,
}

/// The largest number of attestors [`SafetyRegistry::quorum`] and
/// [`SafetyRegistry::is_safe_quorum`] accept in one call.
///
/// Bounding the attestor vector ensures predictable read costs and execution
/// footprint against network transaction invocation limits.
pub const MAX_ATTESTORS: u32 = 16;

/// Quorum status returned by [`SafetyRegistry::quorum`].
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum Quorum {
    /// Quorum met: at least `threshold` responding attestors within `max_age_secs`
    /// agree on the attestation (or are aggregated conservatively), returning
    /// the conservative `Safety`.
    Met(Safety),
    /// Unavailable: strictly fewer responding attestors within `max_age_secs` than `threshold`.
    /// The payload is the count of responding attestors.
    Unavailable(u32),
    /// Contested: at least `threshold` attestors responded within `max_age_secs`, but they
    /// disagree on severity or flags. The payload is the count of responding attestors.
    Contested(u32),
}

/// Event emitted when the current admin withdraws a pending admin transfer.
///
/// Topics: `("cancel_admin", pending_admin)`. A cancellation is the only
/// admin-transfer step that publishes an event, so an indexer can see that a
/// proposed handover was withdrawn instead of inferring it from the next
/// successful `transfer_admin` or `accept_admin`.
#[contractevent(topics = ["cancel_admin"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AdminTransferCancelled {
    /// The pending admin whose proposal was withdrawn.
    #[topic]
    pub pending_admin: Address,
    /// Ledger timestamp of the cancellation.
    pub cancelled_at: u64,
}

/// One element of a [`SafetyRegistry::attest_many`] batch: the arguments of
/// [`SafetyRegistry::attest`], minus the `Env`, grouped so a vector of them can
/// be passed in one call.
///
/// It is deliberately *not* [`Safety`]: that is what storage holds, with the
/// `attested_at` the contract assigns at write time. A caller supplies the
/// three things the off-chain scanner derived and nothing else — there is no
/// field here through which a caller could assert when the attestation was
/// written.
///
/// `Vec<Attestation>` is a supported argument type in soroban-sdk 27: a
/// `#[contracttype]` struct is a UDT, and `Vec<T>` of a UDT is part of the
/// contract-spec type system (`ScVal::Vec`), so the generated client takes a
/// `&Vec<Attestation>` and the spec publishes the struct. This is asserted by
/// the contract compiling and by the batch tests below, which construct one.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Attestation {
    /// The attested asset, identified by its Stellar Asset Contract address.
    pub asset: Address,
    /// Capability severity, `SEVERITY_CLEAR..=SEVERITY_CRITICAL`.
    pub severity: u32,
    /// Bitset of observed mechanics.
    pub flags: u32,
    /// SHA-256 over the canonical evidence bundle, as in [`Safety`].
    pub evidence_hash: BytesN<32>,
}

/// A stored safety attestation for one asset.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Safety {
    /// Capability severity, `SEVERITY_CLEAR..=SEVERITY_CRITICAL`.
    pub severity: u32,
    /// Bitset of observed mechanics.
    pub flags: u32,
    /// SHA-256 over the canonical evidence bundle the scanner fetched. It lets
    /// a verifier prove this attestation corresponds to specific evidence,
    /// rather than trusting the severity number on its own. The exact bytes
    /// hashed are specified by `internal/attest` and reproducible with
    /// `assay attestation -preimage CODE-ISSUER`.
    pub evidence_hash: BytesN<32>,
    /// Ledger timestamp when this attestation was written, obtained from
    /// `env.ledger().timestamp()` (seconds since Unix epoch).
    ///
    /// This is the authoritative timestamp for all on-chain freshness decisions
    /// (`is_safe`, `is_safe_masked`). It reflects the consensus ledger close
    /// time and is independent of off-chain scanner host clocks. See
    /// `docs/timestamps.md`.
    pub attested_at: u64,
    /// The version of the preimage encoding used to produce `evidence_hash`,
    /// so a verifier does not have to guess which encoding produced the hash.
    pub preimage_version: u32,
}

/// Event emitted when an attestation is written or overwritten.
///
/// This event provides an on-chain audit trail so that any overwrite of an
/// attestation can be detected and the previous value reconstructed from
/// chain history.
///
/// Topics:
/// - `"attest"`: static topic identifying the event type
/// - `asset`: the Stellar Asset Contract address (as Address)
///
/// Data:
/// - `previous`: the previous attestation, or `None` if this is the first
///   attestation for this asset
/// - `current`: the new attestation that was written
///
/// Retention: Soroban contract events are retained in ledger history for
/// approximately 1 year (the same retention as ledger entries). Beyond that
/// window, history is not reconstructible from chain alone; an off-chain
/// indexer or archive is required for longer audit trails.
#[contractevent(topics = ["attest"], data_format = "map")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AttestationEvent {
    /// The asset this attestation is for.
    pub asset: Address,
    /// The previous attestation, if any. `None` means this is the first
    /// attestation for this asset.
    pub previous: Option<Safety>,
    /// The new attestation that was written.
    pub current: Safety,
}

/// Event emitted when the current admin proposes a two-step transfer of the
/// admin role to `new_admin` via [`Self::transfer_admin`].
///
/// Topics: `("admin_transfer_proposed", old_admin)`. The proposal is not a
/// transfer until the pending admin calls [`Self::accept_admin`]; this event
/// records the proposal so an indexer can see a pending handover before it
/// completes or is withdrawn. The admin role is unchanged by this event.
#[contractevent(topics = ["admin_transfer_proposed"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AdminTransferProposed {
    /// The current admin who proposed the transfer.
    #[topic]
    pub old_admin: Address,
    /// The proposed successor.
    pub new_admin: Address,
    /// Ledger timestamp of the proposal.
    pub at: u64,
}

/// Event emitted when the pending admin completes a two-step transfer via
/// [`Self::accept_admin`], becoming the new admin.
///
/// Topics: `("admin_transferred", old_admin)`. Together with
/// [`AdminTransferProposed`] and [`AdminTransferCancelled`], this makes every
/// admin-role change observable in the event stream. The stored attestations
/// are untouched; only the admin role moves.
#[contractevent(topics = ["admin_transferred"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AdminTransferred {
    /// The admin who is being replaced.
    #[topic]
    pub old_admin: Address,
    /// The new admin.
    pub new_admin: Address,
    /// Ledger timestamp of the transfer.
    pub at: u64,
}

#[contracttype]
enum DataKey {
    /// Contract admin, the only address permitted to attest.
    Admin,
    /// Proposed new admin during two-step admin transfer.
    PendingAdmin,
    /// Attestation for one asset, keyed by its Stellar Asset Contract address (single-attestor / legacy).
    Safety(Address),
    /// Multi-attestor attestation keyed by (attestor, asset).
    Attestation(Address, Address),
}

#[contracterror]
#[derive(Copy, Clone, Debug, Eq, PartialEq)]
#[repr(u32)]
pub enum Error {
    /// `init` was called on an already-initialized contract.
    AlreadyInitialized = 1,
    /// The contract has no admin yet.
    NotInitialized = 2,
    /// Severity outside `SEVERITY_CLEAR..=SEVERITY_CRITICAL`.
    InvalidSeverity = 3,
    /// An attestation violated the confiscation invariant: the clawback bit is
    /// set but severity is below `SEVERITY_HIGH`. Rejected at write time so a
    /// gate can rely on the invariant at read time.
    InconsistentAttestation = 4,
    /// No pending admin transfer exists to accept.
    NoPendingAdmin = 6,
    /// `revoke` was called for an asset with no attestation in storage.
    /// Returned rather than treated as a no-op so a revocation aimed at the
    /// wrong address fails loudly instead of reporting success while the
    /// attestation it was meant to withdraw still stands.
    NotAttested = 5,
    /// `attest_many` was given more than [`MAX_BATCH_SIZE`] attestations.
    /// Rejected rather than truncated: silently dropping the tail would report
    /// success for assets that were never written, and the off-chain pipeline
    /// would move on believing they had been re-attested.
    BatchTooLarge = 7,
    /// The `asset` argument is known not to name a Stellar Asset Contract: it
    /// is this registry's own address, the admin's address, or an address
    /// whose type is not a contract at all (an account, a muxed account, a
    /// claimable balance or a liquidity pool id).
    ///
    /// Rejected so a fat-fingered write cannot key `Safety(...)` under an
    /// address nobody meant, pay rent on it and publish an `Attested` event
    /// naming it. Which contract is at a given address is deliberately NOT
    /// checked here — see [`validate_asset_address`] for the decision and for
    /// what the caller owns instead (#349).
    InvalidAsset = 8,
}

/// Extends the contract instance's TTL to the network maximum. Extending the
/// instance also extends the contract code entry, so the admin and the wasm
/// stay live as long as the registry is being written to.
///
/// The maximum is read from the host rather than hard-coded: archival
/// parameters are network configuration and change by validator vote. See
/// "Entry lifetime" in `docs/deployment.md` for why this runs on writes only.
fn extend_instance(env: &Env) {
    let max = env.storage().max_ttl();
    env.storage().instance().extend_ttl(max, max);
}

/// Extends one attestation entry's TTL to the network maximum.
///
/// `threshold == extend_to`, so this always extends: a fresh write starts at
/// the network minimum, far below the maximum.
fn extend_attestation(env: &Env, key: &DataKey) {
    let max = env.storage().max_ttl();
    env.storage().persistent().extend_ttl(key, max, max);
}

/// The write-time validation shared by [`SafetyRegistry::attest`] and
/// [`SafetyRegistry::attest_many`].
///
/// It is one function rather than two copies on purpose: the batch path must
/// not be able to accept anything the single path refuses, and the only way to
/// be sure of that is for both to call the same code. A reviewer can see the
/// invariant is not bypassable by reading this function's two call sites.
fn validate_attestation(
    env: &Env,
    asset: &Address,
    admin: &Address,
    severity: u32,
    flags: u32,
) -> Result<(), Error> {
    validate_asset_address(env, asset, admin)?;
    if severity > SEVERITY_CRITICAL {
        return Err(Error::InvalidSeverity);
    }
    if flags & CONFISCATION_MASK != 0 && severity < SEVERITY_HIGH {
        return Err(Error::InconsistentAttestation);
    }
    Ok(())
}

/// Rejects an `asset` address that is known not to name a Stellar Asset
/// Contract, before anything is keyed under it (#349).
///
/// # What is checked, and why each check is worth its cost
///
/// Storage is keyed by any `Address`, so a write aimed at the wrong address is
/// fail-closed for the real asset — `get_safety` on the real SAC still returns
/// `None` and every gate still refuses — but it is not harmless: it pays rent
/// on an entry nobody will ever read as this asset, publishes an `Attested`
/// event naming an address that is not this asset, and leaves an indexer
/// something to explain. Three rules answer that. The first two are plain
/// comparisons; the third reads the address's strkey, one host call and one
/// small allocation, with no ledger access:
///
/// * **Not the registry's own address.** `attest` on the registry would key
///   `DataKey::Safety(registry)` inside the registry's own storage, and the
///   resulting event would read as the registry attesting itself.
/// * **Not the admin's address.** The admin is the writer, never the subject.
///   It is normally an account, so the rule below already covers it, but the
///   admin is an arbitrary `Address` — `init` accepts any — and the comparison
///   stays correct when it is a contract (a multisig, say).
/// * **Not a non-contract address.** A Stellar Asset Contract is a contract,
///   so an account (`G…`), a muxed account (`M…`), a claimable balance or a
///   liquidity pool id can all be ruled out by the address's strkey version
///   byte (SEP-23): a contract address is `C…`, and nothing else is. This is
///   the fat-finger case the check exists for: pasting an issuer account
///   where a SAC address belongs.
///
/// # What is deliberately not checked: caller responsibility
///
/// Whether the contract at `asset` really is the SAC for the CODE-ISSUER the
/// scanner scanned. Answering that on-chain would mean invoking the candidate
/// contract and trusting whatever it says about itself, which is neither cheap
/// nor self-authenticating: an impostor contract answers for itself. The same
/// conclusion is reached off-chain instead, by resolving CODE-ISSUER to its SAC
/// with `stellar contract id asset` — which is what `make attest` does before
/// it ever signs anything. So an impostor *contract* address passes this check
/// and the attester owns having resolved the right one; the stored claim is
/// still what the scanner concluded for the asset it scanned, bound by
/// `evidence_hash`.
fn validate_asset_address(env: &Env, asset: &Address, admin: &Address) -> Result<(), Error> {
    if *asset == env.current_contract_address() || *asset == *admin {
        return Err(Error::InvalidAsset);
    }
    // The off-host `ScAddress::from` conversion is compiled out of wasm builds,
    // so the type is read from the strkey instead: `C…` is a contract, and
    // every other version byte — account `G…`, muxed `M…`, and the tags for
    // claimable balances and liquidity pools — cannot name a SAC.
    match asset.to_string().to_bytes().get(0) {
        Some(b'C') => Ok(()),
        _ => Err(Error::InvalidAsset),
    }
}

/// Writes one attestation and publishes its event. Shared by both write paths
/// so a batched element and a single call cannot diverge in what they store or
/// in what an indexer observes.
fn write_attestation(
    env: &Env,
    asset: Address,
    severity: u32,
    flags: u32,
    evidence_hash: BytesN<32>,
    attested_at: u64,
    preimage_version: u32,
) {
    let key = DataKey::Safety(asset.clone());
    let safety = Safety {
        severity,
        flags,
        evidence_hash,
        attested_at,
        preimage_version,
    };
    env.storage().persistent().set(&key, &safety);
    extend_attestation(env, &key);

    // Publish after the write. Emitting before would let a storage failure
    // produce a visible "attest" for an attestation that does not exist;
    // emitting after means indexers observe writes that actually happened.
    Attested {
        asset,
        severity,
        flags,
        attested_at,
    }
    .publish(env);
}

/// The largest number of attestations [`SafetyRegistry::attest_many`] accepts
/// in one call.
///
/// The cap is derived from the network's transaction resource limits rather
/// than picked for tidiness: it is the tightest of them divided down to leave
/// headroom. The limits were read live on 2026-09-27 with
/// `stellar network settings --network testnet` (protocol 28), and match
/// `InvocationResourceLimits::mainnet()` in soroban-sdk 27.0.5. The cost column
/// is a real 50-element batch measured through those same limits by
/// `batch_size_bound_is_measured_and_holds_headroom`:
///
/// | Limit | Live network | A full 50-element batch |
/// | --- | --- | --- |
/// | **contract event bytes** | **16 384** | **10 000 (61%) — the binding limit** |
/// | ledger entries written | 200 | 51 (26%) |
/// | transaction footprint entries | 400 | 105 (26%) |
/// | bytes written | 132 096 | 14 072 (11%) |
/// | instructions | 400 000 000 | 3 275 513 (0.8%) |
/// | memory | 41 943 040 | 514 609 (1.2%) |
///
/// # What actually binds
///
/// Each element publishes its own `Attested` event, and one such event costs
/// exactly 200 bytes. The event budget therefore admits at most 81 elements
/// no matter how frugal the writes are — 81 is the hard ceiling, and 100 was
/// measured failing at `20000 > 16384`. 50 sits at 61% of that budget, leaves
/// room for the event schema to grow before the cap is wrong, and costs a
/// quarter of every other limit, including the write budget one would have
/// guessed was binding.
///
/// Per-asset events are kept rather than collapsed into one batch-sized event
/// precisely because of that cost: a single event would raise the ceiling, but
/// it would take away the property that an indexer can subscribe by asset
/// (`("attest", asset)` topics) and see every write to that asset without
/// decoding batch bodies. A higher cap is worth less than that.
///
/// `batch_size_bound_is_measured_and_holds_headroom` runs a full batch with the
/// SDK's own mainnet resource enforcement switched on — which `Env::default`
/// enables — and asserts both that it fits every limit and that the per-event
/// cost has not moved, so the cap cannot quietly drift out from under the
/// contract when the code below it changes.
pub const MAX_BATCH_SIZE: u32 = 50;

#[contract]
pub struct SafetyRegistry;

#[contractimpl]
impl SafetyRegistry {
    /// Sets the admin permitted to write attestations.
    pub fn init(env: Env, admin: Address) -> Result<(), Error> {
        if env.storage().instance().has(&DataKey::Admin) {
            return Err(Error::AlreadyInitialized);
        }
        env.storage().instance().set(&DataKey::Admin, &admin);
        extend_instance(&env);
        Ok(())
    }

    /// Initiates a two-step transfer of the admin role to `new_admin`.
    ///
    /// Requires authorization from the current admin. The transfer takes effect
    /// only when `new_admin` calls [`Self::accept_admin`]. Two-step transfer
    /// avoids transferring admin control to an unowned address or typo.
    pub fn transfer_admin(env: Env, new_admin: Address) -> Result<(), Error> {
        let admin: Address = env
            .storage()
            .instance()
            .get(&DataKey::Admin)
            .ok_or(Error::NotInitialized)?;
        admin.require_auth();

        env.storage()
            .instance()
            .set(&DataKey::PendingAdmin, &new_admin);

        AdminTransferProposed {
            old_admin: admin.clone(),
            new_admin: new_admin.clone(),
            at: env.ledger().timestamp(),
        }
        .publish(&env);
        extend_instance(&env);
        Ok(())
    }

    /// Completes a two-step admin transfer, transferring the admin role to the
    /// pending admin.
    ///
    /// Requires authorization from the pending admin. After completion, the old
    /// admin can no longer attest or manage admin transfers.
    pub fn accept_admin(env: Env) -> Result<(), Error> {
        let pending: Address = env
            .storage()
            .instance()
            .get(&DataKey::PendingAdmin)
            .ok_or(Error::NoPendingAdmin)?;
        pending.require_auth();

        let old_admin: Address = env
            .storage()
            .instance()
            .get(&DataKey::Admin)
            .ok_or(Error::NotInitialized)?;

        env.storage().instance().set(&DataKey::Admin, &pending);
        env.storage().instance().remove(&DataKey::PendingAdmin);

        AdminTransferred {
            old_admin: old_admin.clone(),
            new_admin: pending.clone(),
            at: env.ledger().timestamp(),
        }
        .publish(&env);
        extend_instance(&env);
        Ok(())
    }

    /// Withdraws a pending admin transfer proposed by [`Self::transfer_admin`].
    ///
    /// Requires authorization from the current admin. The admin role is
    /// unchanged; only the pending proposal is cleared, so the current admin
    /// keeps attesting and the previously pending address can no longer call
    /// [`Self::accept_admin`].
    ///
    /// Cancelling when no transfer is pending returns [`Error::NoPendingAdmin`]
    /// rather than succeeding quietly, so an operator learns the proposal was
    /// already absent. Emits an [`AdminTransferCancelled`] event on success.
    pub fn cancel_admin_transfer(env: Env) -> Result<(), Error> {
        let admin: Address = env
            .storage()
            .instance()
            .get(&DataKey::Admin)
            .ok_or(Error::NotInitialized)?;

        // Report a missing proposal before requiring auth, so "nothing to
        // cancel" is always the typed error rather than an auth failure. This
        // is safe: the path changes no state.
        let pending: Address = env
            .storage()
            .instance()
            .get(&DataKey::PendingAdmin)
            .ok_or(Error::NoPendingAdmin)?;
        admin.require_auth();

        env.storage().instance().remove(&DataKey::PendingAdmin);
        extend_instance(&env);

        // Published after the removal, for the same reason as in `attest`: an
        // observer must never see a cancellation that did not happen.
        AdminTransferCancelled {
            pending_admin: pending,
            cancelled_at: env.ledger().timestamp(),
        }
        .publish(&env);
        Ok(())
    }

    /// Reads the current contract admin, if initialized.
    ///
    /// Returns `None` before [`Self::init`] has been called, and `Some(Address)`
    /// afterwards.
    pub fn admin(env: Env) -> Option<Address> {
        env.storage().instance().get(&DataKey::Admin)
    }

    /// Reads the pending admin during a two-step admin transfer, if any.
    ///
    /// Returns `Some(Address)` between [`Self::transfer_admin`] and [`Self::accept_admin`],
    /// and `None` when no transfer is pending.
    pub fn pending_admin(env: Env) -> Option<Address> {
        env.storage().instance().get(&DataKey::PendingAdmin)
    }

    /// Writes an attestation for `asset`, identified by its SAC address.
    ///
    /// `severity`, `flags`, and `evidence_hash` come from the off-chain
    /// scanner: `assay attestation CODE-ISSUER` derives all three from a live
    /// scan, and `make attest` submits them. Validation here is not a
    /// formality: it enforces at write time the invariants that
    /// [`Self::is_safe`] relies on at read time. It also refuses an `asset`
    /// that is known not to name a Stellar Asset Contract — this registry's
    /// own address, the admin's address, or a non-contract address — with
    /// [`Error::InvalidAsset`]; see [`validate_asset_address`] for what that
    /// check can and cannot establish (#349).
    ///
    /// Emits an [`AttestationEvent`] with the previous value (if any) and the
    /// new value, providing an on-chain audit trail. See the event
    /// documentation for retention semantics.
    pub fn attest(
        env: Env,
        asset: Address,
        severity: u32,
        flags: u32,
        evidence_hash: BytesN<32>,
    ) -> Result<(), Error> {
        let admin: Address = env
            .storage()
            .instance()
            .get(&DataKey::Admin)
            .ok_or(Error::NotInitialized)?;
        admin.require_auth();

        validate_attestation(&env, &asset, &admin, severity, flags)?;

        let preimage_version = 4; // v4 encoding with scanner identity

        write_attestation(
            &env,
            asset,
            severity,
            flags,
            evidence_hash,
            env.ledger().timestamp(),
            preimage_version,
        );
        extend_instance(&env);
        Ok(())
    }

    /// Writes attestations for many assets in one transaction, so the pipeline
    /// can cover more than a curated few per ledger of fee budget.
    ///
    /// Each element is written exactly as [`Self::attest`] writes it: the same
    /// key, the same value, the same `Attested` event, and the same validation,
    /// which both paths get from the same `validate_attestation`. A read after
    /// a batch is therefore indistinguishable from a read after the equivalent
    /// sequence of single calls — the property
    /// `batch_reads_match_single_write_path` pins.
    ///
    /// # All-or-nothing
    ///
    /// A batch is applied whole or not at all. Every element is validated
    /// before the first write, so a batch containing one bad element returns
    /// [`Error::InvalidAsset`], [`Error::InvalidSeverity`] or
    /// [`Error::InconsistentAttestation`] and stores nothing: there is no view
    /// in which some assets were re-attested and others silently were not.
    /// Nothing is truncated or skipped to make a partly-bad batch fit.
    ///
    /// The host already rolls a transaction back on failure, so a batch could
    /// not half-commit even without this. The two-phase shape buys two things
    /// on top of that. The failure is *typed and deterministic* — an error a
    /// caller can branch on, rather than "some write in the middle exceeded a
    /// resource limit" — and it is *cheap*: a batch whose last element is bad
    /// does not first pay to write the 49 before it.
    ///
    /// # Bound
    ///
    /// At most [`MAX_BATCH_SIZE`] attestations are accepted; a longer vector
    /// returns [`Error::BatchTooLarge`] and writes nothing. See
    /// [`MAX_BATCH_SIZE`] for where the cap comes from and
    /// `docs/contract-interface.md` for the measured headroom. A batch larger
    /// than the cap is an error rather than a truncation on purpose: a caller
    /// that sent 500 attestations and got back `Ok` would believe all 500 were
    /// recorded.
    ///
    /// # Empty batches
    ///
    /// An empty batch succeeds and does nothing: no storage write, no event,
    /// and (unlike a non-empty batch) no instance TTL extension. That is the
    /// honest reading of all-or-nothing over zero elements, and it lets the
    /// pipeline call this with whatever the scan produced without the caller
    /// special-casing "nothing changed this round". It is named in the tests
    /// so it stays a decision rather than an accident.
    ///
    /// # One timestamp per batch
    ///
    /// Every element written by one call carries the same `attested_at`: the
    /// timestamp of the ledger the batch landed in. `attested_at` records when
    /// the write happened, not when each scan ran, and the elements of one
    /// call were written at the same instant. A consumer comparing two
    /// assets' freshness from the same batch is comparing the same number.
    ///
    /// If the same asset appears twice, both elements are applied in order and
    /// both events are published; the second write wins. That is what the same
    /// two calls to [`Self::attest`] would do, and there is nothing to
    /// reconcile — the caller asserted the later value last.
    pub fn attest_many(env: Env, attestations: Vec<Attestation>) -> Result<(), Error> {
        let admin: Address = env
            .storage()
            .instance()
            .get(&DataKey::Admin)
            .ok_or(Error::NotInitialized)?;
        // Authorized even for an empty batch: the reject path must not differ
        // by length, and "who may call this at all" is a property of the
        // entrypoint rather than of what it was handed.
        admin.require_auth();

        if attestations.len() > MAX_BATCH_SIZE {
            return Err(Error::BatchTooLarge);
        }

        // Phase one: validate every element before anything is written. This is
        // what makes the batch all-or-nothing at the contract level rather than
        // only by the host's transaction rollback.
        for a in attestations.iter() {
            validate_attestation(&env, &a.asset, &admin, a.severity, a.flags)?;
        }

        if attestations.is_empty() {
            return Ok(());
        }

        // Phase two: write. One ledger timestamp for the whole batch, read once
        // so every element agrees on it.
        let attested_at = env.ledger().timestamp();
        let preimage_version = 4; // v4 encoding with scanner identity
        for a in attestations.iter() {
            write_attestation(
                &env,
                a.asset.clone(),
                a.severity,
                a.flags,
                a.evidence_hash.clone(),
                attested_at,
                preimage_version,
            );
        }
        extend_instance(&env);
        Ok(())
    }

    /// Withdraws the attestation for `asset`, restoring the never-attested
    /// state: afterwards `get_safety` returns `None` and every gate fails
    /// closed on the asset.
    ///
    /// This is a retraction, not a new claim. Overwriting a wrong attestation
    /// with a higher severity asserts something the scanner never concluded;
    /// revoking says only that the previous claim no longer stands. Until
    /// revocation existed, overwriting was the only remedy for a wrong
    /// attestation — which asserted a new claim rather than retracting one.
    ///
    /// Only the admin may revoke, mirroring `attest`. Revoking an asset with no
    /// attestation returns [`Error::NotAttested`] and changes nothing, so an
    /// operator who aims at the wrong address learns the entry was absent
    /// rather than being told the revocation succeeded. The asset can be
    /// attested again afterwards.
    ///
    /// Revoked and never-attested are deliberately indistinguishable to
    /// `get_safety`: both return `None`. The contract keeps no tombstone, so it
    /// does not claim to tell them apart. The `Revoked` event is the only place
    /// the two differ off-chain. See `docs/contract-interface.md`.
    pub fn revoke(env: Env, asset: Address) -> Result<(), Error> {
        let admin: Address = env
            .storage()
            .instance()
            .get(&DataKey::Admin)
            .ok_or(Error::NotInitialized)?;
        admin.require_auth();

        let key = DataKey::Safety(asset.clone());
        if !env.storage().persistent().has(&key) {
            return Err(Error::NotAttested);
        }
        env.storage().persistent().remove(&key);
        extend_instance(&env);

        // Published after the removal, for the same reason as in `attest`.
        Revoked {
            asset,
            revoked_at: env.ledger().timestamp(),
        }
        .publish(&env);
        Ok(())
    }

    /// Reads the attestation for `asset`.
    ///
    /// Returns `None` when the asset has never been attested. That case is
    /// deliberately distinguishable from an attestation of `SEVERITY_CLEAR`:
    /// collapsing the two would make every unknown asset read as safe, which is
    /// the single worst failure this contract could have.
    ///
    /// Archived entries (TTL expired) are automatically restored per CAP-0066 /
    /// Protocol 23 when accessed. After restoration, the entry returns
    /// `Some(Safety)` with the original `attested_at` timestamp. This makes
    /// archived entries distinguishable from never-attested ones:
    /// - Never attested: returns `None`
    /// - Archived (restored): returns `Some(Safety)` with original `attested_at`
    ///
    /// The `attest` function extends the TTL to the maximum on every write, so
    /// archival should not occur in normal operation. Freshness is enforced by
    /// the caller via `max_age_secs` on `is_safe`, not by storage expiry.
    /// `None` also covers a revoked attestation: storage keeps no tombstone,
    /// so a caller cannot tell revoked from never attested. Both mean "no
    /// claim stands", and both fail closed.
    ///
    /// Reads do not extend TTL. An archived entry is never read as `None`:
    /// the host either restores it (and it reads with its original
    /// `attested_at`) or fails the transaction. See `docs/deployment.md`.
    pub fn get_safety(env: Env, asset: Address) -> Option<Safety> {
        env.storage().persistent().get(&DataKey::Safety(asset))
    }

    /// Returns the contract's interface version, bumped on every ABI change.
    ///
    /// A consumer or a drift script can call this to learn what the contract
    /// is rather than inferring it from a wasm hash. See [`INTERFACE_VERSION`]
    /// for the bump rule.
    pub fn version(_env: Env) -> u32 {
        INTERFACE_VERSION
    }

    /// Reads the attestations for `assets`, returning one `Option<Safety>` per
    /// asset in the same order the assets were given.
    ///
    /// An asset that was never attested, or whose attestation was revoked,
    /// reads as `None`. A missing asset never fails the whole call: only the
    /// bound can reject it.
    ///
    /// # Bound
    ///
    /// At most [`MAX_BATCH_SIZE`] assets are accepted; a longer vector returns
    /// [`Error::BatchTooLarge`] and reads nothing. The bound mirrors
    /// [`Self::attest_many`]'s so one number governs both directions of the
    /// batch, and the read cost at the bound is pinned by
    /// `get_safety_many_cost_at_the_bound`.
    pub fn get_safety_many(env: Env, assets: Vec<Address>) -> Result<Vec<Option<Safety>>, Error> {
        if assets.len() > MAX_BATCH_SIZE {
            return Err(Error::BatchTooLarge);
        }

        let mut results = Vec::new(&env);
        for asset in assets.iter() {
            results.push_back(Self::get_safety(env.clone(), asset.clone()));
        }
        Ok(results)
    }

    /// The fail-closed gate helper.
    ///
    /// Returns `true` only when an attestation exists, is fresh enough, and is
    /// at or below `max_severity`. Every other path returns `false`: never
    /// attested, stale, too severe, or inconsistent. The safe answer is the
    /// default, so a caller that gets the arguments wrong blocks rather than
    /// admits.
    ///
    /// Failure modes (all return `false`):
    /// - Never attested: `get_safety` returns `None`
    /// - Stale: `attested_at` older than `max_age_secs`
    /// - Too severe: `severity > max_severity`
    /// - Inconsistent: clawback capability attested below `SEVERITY_HIGH`
    ///
    /// A caller that needs to diagnose why a gate rejected can call
    /// `get_safety` directly: `None` means never attested; `Some(Safety)`
    /// with an old `attested_at` means the attestation has lapsed.
    ///
    /// `max_age_secs` of 0 disables the freshness requirement.
    pub fn is_safe(env: Env, asset: Address, max_severity: u32, max_age_secs: u64) -> bool {
        let Some(safety) = Self::get_safety(env.clone(), asset) else {
            return false; // never attested: fail closed
        };

        if safety.severity > max_severity {
            return false;
        }

        // Defence in depth: attest() rejects this, but a gate must not depend
        // on the writer having been correct.
        if safety.flags & CONFISCATION_MASK != 0 && max_severity < SEVERITY_HIGH {
            return false;
        }

        if max_age_secs > 0 {
            let now = env.ledger().timestamp();
            // saturating_sub avoids underflow if an attestation carries a
            // timestamp ahead of the current ledger.
            if now.saturating_sub(safety.attested_at) > max_age_secs {
                return false;
            }
        }

        true
    }

    /// The mask-based fail-closed gate.
    ///
    /// Returns `true` only when an attestation exists, is fresh enough, and
    /// carries none of the bits set in `forbidden_mask`. Every other path
    /// returns `false`: never attested, stale, or forbidden bit set. An empty
    /// `forbidden_mask` still requires an attestation — an unattested asset
    /// must never read as safe, even with a policy that forbids nothing.
    ///
    /// Severity is a total order; policies are not. Severity-based gating with
    /// `is_safe` collapses two independent questions ("can they freeze it?"
    /// "can they take it?") onto one axis. A caller that can tolerate a freeze
    /// but never a confiscation should gate on `POLICY_MASK_CONFISCATION_ONLY`;
    /// a custody product refusing both should gate on
    /// `POLICY_MASK_FREEZE_INCLUSIVE`. See `docs/contract-interface.md`.
    ///
    /// `max_age_secs` of 0 disables the freshness requirement.
    pub fn is_safe_masked(
        env: Env,
        asset: Address,
        forbidden_mask: u32,
        max_age_secs: u64,
    ) -> bool {
        let Some(safety) = Self::get_safety(env.clone(), asset) else {
            return false; // never attested: fail closed
        };

        if safety.flags & forbidden_mask != 0 {
            return false;
        }

        if max_age_secs > 0 {
            let now = env.ledger().timestamp();
            if now.saturating_sub(safety.attested_at) > max_age_secs {
                return false;
            }
        }

        true
    }

    /// Reads the attestation written by a specific `attestor` for `asset`.
    ///
    /// Checks multi-attestor storage `Attestation(attestor, asset)`. If not present
    /// and `attestor` is the contract admin, falls back to legacy `Safety(asset)`
    /// storage to support the migration path.
    pub fn get_attestation(env: Env, attestor: Address, asset: Address) -> Option<Safety> {
        let key = DataKey::Attestation(attestor.clone(), asset.clone());
        if let Some(safety) = env.storage().persistent().get(&key) {
            return Some(safety);
        }
        if let Some(admin) = Self::admin(env.clone()) {
            if admin == attestor {
                return Self::get_safety(env, asset);
            }
        }
        None
    }

    /// Evaluates quorum across a consumer-chosen set of `attestors` for `asset`.
    ///
    /// Attestors are deduplicated before evaluation.
    /// Returns:
    /// - `Quorum::Unavailable(responding_count)` if responding_count < threshold, or threshold == 0, or threshold > deduplicated_attestors.len(), or deduplicated_attestors.len() > MAX_ATTESTORS
    /// - `Quorum::Contested(responding_count)` if responding_count >= threshold, but responding attestors differ in severity or flags
    /// - `Quorum::Met(Safety)` if responding_count >= threshold and responding attestors agree on severity and flags
    pub fn quorum(
        env: Env,
        asset: Address,
        attestors: Vec<Address>,
        threshold: u32,
        max_age_secs: u64,
    ) -> Quorum {
        // Deduplicate attestors
        let mut deduped = Vec::new(&env);
        for a in attestors.iter() {
            let mut found = false;
            for d in deduped.iter() {
                if a == d {
                    found = true;
                    break;
                }
            }
            if !found {
                deduped.push_back(a);
            }
        }

        let deduped_len = deduped.len();
        if threshold == 0 || threshold > deduped_len || deduped_len > MAX_ATTESTORS {
            return Quorum::Unavailable(0);
        }

        let now = env.ledger().timestamp();
        let mut responding_count = 0u32;
        let mut max_severity = 0u32;
        let mut union_flags = 0u32;
        let mut min_severity = u32::MAX;
        let mut first_flags = 0u32;
        let mut latest_timestamp = 0u64;
        let mut evidence_hash = BytesN::from_array(&env, &[0u8; 32]);
        // The aggregate reports the encoding of the hash it carries: both come
        // from the same responding attestation.
        let mut preimage_version = 0u32;
        let mut all_agree = true;

        for attestor in deduped.iter() {
            if let Some(s) = Self::get_attestation(env.clone(), attestor, asset.clone()) {
                if max_age_secs == 0 || now.saturating_sub(s.attested_at) <= max_age_secs {
                    if responding_count == 0 {
                        first_flags = s.flags;
                        evidence_hash = s.evidence_hash.clone();
                        preimage_version = s.preimage_version;
                    } else if s.severity != max_severity || s.flags != first_flags {
                        all_agree = false;
                    }

                    responding_count += 1;
                    if s.severity > max_severity {
                        max_severity = s.severity;
                    }
                    if s.severity < min_severity {
                        min_severity = s.severity;
                    }
                    union_flags |= s.flags;
                    if s.attested_at > latest_timestamp {
                        latest_timestamp = s.attested_at;
                        evidence_hash = s.evidence_hash.clone();
                        preimage_version = s.preimage_version;
                    }
                }
            }
        }

        if responding_count < threshold {
            Quorum::Unavailable(responding_count)
        } else if !all_agree || max_severity != min_severity {
            Quorum::Contested(responding_count)
        } else {
            Quorum::Met(Safety {
                severity: max_severity,
                flags: union_flags,
                evidence_hash,
                attested_at: latest_timestamp,
                preimage_version,
            })
        }
    }

    /// Evaluates whether an asset passes a severity ceiling under consumer-chosen quorum with conservative aggregation.
    ///
    /// Conservative aggregation rule: requires at least `threshold` responding attestors within `max_age_secs`.
    /// The aggregate result takes the **highest severity** and **union of flags** among all responding attestors.
    /// Returns `true` only if quorum is reached, aggregate severity <= `max_severity`, and the confiscation invariant holds
    /// (or max_severity >= SEVERITY_HIGH).
    pub fn is_safe_quorum(
        env: Env,
        asset: Address,
        attestors: Vec<Address>,
        threshold: u32,
        max_severity: u32,
        max_age_secs: u64,
    ) -> bool {
        // Deduplicate attestors
        let mut deduped = Vec::new(&env);
        for a in attestors.iter() {
            let mut found = false;
            for d in deduped.iter() {
                if a == d {
                    found = true;
                    break;
                }
            }
            if !found {
                deduped.push_back(a);
            }
        }

        let deduped_len = deduped.len();
        if threshold == 0 || threshold > deduped_len || deduped_len > MAX_ATTESTORS {
            return false;
        }

        let now = env.ledger().timestamp();
        let mut responding_count = 0u32;
        let mut agg_severity = 0u32;
        let mut agg_flags = 0u32;

        for attestor in deduped.iter() {
            if let Some(s) = Self::get_attestation(env.clone(), attestor, asset.clone()) {
                if max_age_secs == 0 || now.saturating_sub(s.attested_at) <= max_age_secs {
                    responding_count += 1;
                    if s.severity > agg_severity {
                        agg_severity = s.severity;
                    }
                    agg_flags |= s.flags;
                }
            }
        }

        if responding_count < threshold {
            return false;
        }

        if agg_severity > max_severity {
            return false;
        }

        if agg_flags & CONFISCATION_MASK != 0 && max_severity < SEVERITY_HIGH {
            return false;
        }

        true
    }
}

#[cfg(test)]
mod test;
