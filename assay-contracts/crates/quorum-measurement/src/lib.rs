#![no_std]
//! Quorum measurement scratch contract.
//! Prototypes the Scheme C quorum interface from multi-attestor.md and sep-asset-safety-attestation.md.
//! Measures CPU, memory, and read-entry costs of reading N attestations and aggregating them conservatively.
//!
//! # Interface
//!
//! ```rust
//! pub fn quorum(
//!     env: Env,
//!     asset: Address,
//!     attestors: Vec<Address>,
//!     threshold: u32,
//!     max_age_secs: u64,
//! ) -> Quorum;
//! ```
//!
! ```rust
//! pub fn is_safe_quorum(
//!     env: Env,
//!     asset: Address,
//!     attestors: Vec<Address>,
//!     threshold: u32,
//!     max_severity: u32,
//!     max_age_secs: u64,
//! ) -> bool;
//! ```

use soroban_sdk::{contract, contracterror, contracttype, Address, Env, Vec};
use soroban_sdk::symbol_short;

/// The aggregation result of a quorum read.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum Quorum {
    /// At least `threshold` responding attestors; the conservative result.
    Met(Safety),
    /// Fewer than `threshold` responding: an outage.
    Unavailable(u32),
    /// `threshold` or more responding, but they differ: Contested with conservative result.
    Contested(u32),
}

/// One stored safety attestation, keyed by (attestor, asset).
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Safety {
    /// Capability severity, `SEVERITY_CLEAR..=SEVERITY_CRITICAL`.
    pub severity: u32,
    /// Bitset of observed mechanics.
    pub flags: u32,
    /// SHA-256 over the canonical evidence bundle.
    pub evidence_hash: [u8; 32],
    /// Ledger timestamp of the write.
    pub attested_at: u64,
}

/// Named keys in contract storage.
#[contracttype]
enum DataKey {
    /// Attestation for one (attestor, asset) pair.
    Attestation(Address, Address),
}

/// Events emitted by the contract.
#[contractevent(topics = ["quorum"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct QuorumComputed {
    /// The asset the quorum was computed for.
    pub asset: Address,
    /// The result of the quorum aggregation.
    pub result: Quorum,
    /// The number of responding attestators (fresh within max_age_secs).
    pub responding: u32,
    /// The threshold that was checked.
    pub threshold: u32,
}

/// Events emitted when an attestation is written.
#[contractevent(topics = ["attest"])]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Attested {
    /// The attestor address.
    #[topic]
    pub attestor: Address,
    /// The attested asset.
    #[topic]
    pub asset: Address,
    /// Capability severity written for the asset.
    pub severity: u32,
    /// Mechanic bitset written for the asset.
    pub flags: u32,
    /// Ledger timestamp of the write.
    pub attested_at: u64,
}

/// Mechanics bit constants (must match the registry).
pub const MECH_AUTH_REQUIRED: u32 = 1 << 0;
pub const MECH_AUTH_REVOCABLE: u32 = 1 << 1;
pub const MECH_CLAWBACK_ENABLED: u32 = 1 << 2;
pub const MECH_FLAGS_LOCKED: u32 = 1 << 3;
pub const MECH_DOMAIN_UNVERIFIED: u32 = 1 << 4;
pub const MECH_BLOCKLISTED: u32 = 1 << 5;

/// Capability mask: auth_required | auth_revocable | auth_clawback_enabled.
pub const CAPABILITY_MASK: u32 = MECH_AUTH_REQUIRED | MECH_AUTH_REVOCABLE | MECH_CLAWBACK_ENABLED;

/// Severity constants (must match the registry).
pub const SEVERITY_CLEAR: u32 = 0;
pub const SEVERITY_LOW: u32 = 1;
pub const SEVERITY_MEDIUM: u32 = 2;
pub const SEVERITY_HIGH: u32 = 3;
pub const SEVERITY_CRITICAL: u32 = 4;

/// Maximum number of attestors allowed in a quorum call.
/// This bounds the read budget to a predictable value.
const MAX_ATTESTORS: u32 = 16;

#[contractimpl]
impl quorum_measurement {
    /// Writes an attestation from an attestor for an asset.
    /// Only the attestor may call this (auth requires).
    pub fn attest(env: Env, attestor: Address, asset: Address, severity: u32, flags: u32) -> Result<(), Error> {
        // Authorize the attestor
        attestor.require_auth();

        // Validate severity and flags
        if severity > SEVERITY_CRITICAL {
            return Err(Error::InvalidSeverity);
        }
        if flags & CONFISCATION_MASK != 0 && severity < SEVERITY_HIGH {
            return Err(Error::InconsistentAttestation);
        }

        // Store the attestation keyed by (attestor, asset)
        let key = DataKey::Attestation(attestor.clone(), asset.clone());
        let safety = Safety {
            severity,
            flags,
            evidence_hash: [0u8; 32], // placeholder - in real use derived from evidence
            attested_at: env.ledger().timestamp(),
        };
        env.storage().persistent().set(&key, &safety);

        // Publish event
        Attested {
            attestor: attestor.clone(),
            asset: asset.clone(),
            severity,
            flags,
            attested_at: env.ledger().timestamp(),
        }
        .publish(&env);

        extend_instance(&env);
        Ok(())
    }

    /// Reads attestations for the given attestors and asset, and computes the quorum.
    ///
    /// Aggregation rule (conservative max/union):
    /// - Require at least `threshold` responding attestors (fresh within max_age_secs).
    /// - The result takes the highest severity and the union of flags among all
    ///   responding attestors.
    /// - Report Contested when responders differ, alongside the conservative result.
    /// - Fewer than threshold responding -> Unavailable.
    /// - threshold > attestors.len() or threshold == 0 -> error.
    pub fn quorum(
        env: Env,
        asset: Address,
        attestors: Vec<Address>,
        threshold: u32,
        max_age_secs: u64,
    ) -> Quorum {
        // Validate threshold
        let unique_attestors = Self::deduplicate_attestors(&attestors);
        if threshold == 0 {
            panic!("Quorum: threshold must be > 0");
        }
        if threshold > unique_attestors.len() as u32 {
            panic!("Quorum: threshold {} exceeds number of unique attestors {}", threshold, unique_attestors.len());
        }
        if unique_attestors.len() as u32 > MAX_ATTESTORS {
            panic!("Quorum: too many attestors (max {})", MAX_ATTESTORS);
        }

        // Count responding attestors (fresh within max_age_secs)
        let now = env.ledger().timestamp();
        let mut responding_count: u32 = 0;
        let mut highest_severity: u32 = SEVERITY_CLEAR;
        let mut union_flags: u32 = 0;

        for attestor in &unique_attestors {
            // Read the attestation for this (attestor, asset) pair
            let key = DataKey::Attestation(attestor.clone(), asset.clone());
            if let Some(safety) = env.storage().persistent().get(&key) {
                // Check freshness
                if now.saturating_sub(safety.attested_at) <= max_age_secs {
                    responding_count += 1;

                    // Conservative max/union: take the highest severity
                    if safety.severity > highest_severity {
                        highest_severity = safety.severity;
                    }
                    // Union of flags
                    union_flags |= safety.flags;
                }
            }
        }

        // Determine the quorum result
        if responding_count < threshold {
            // Not enough responding attestors
            Quorum::Unavailable(responding_count)
        } else {
            // Enough responding - check if they agree (differ on severity or flags)
            // Conservative aggregation: always return Met with the worst case among responders
            // Contested is reported alongside the conservative result
            let result_safety = Safety {
                severity: highest_severity,
                flags: union_flags,
                evidence_hash: [0u8; 32],
                attested_at: 0,
            };

            // Check if there's disagreement (more than one distinct severity or flag combination)
            // For conservative aggregation, we still report Met but with the conservative result
            // The Contested state captures that responders differed
            // We determine Contested if there were multiple different severity or flag sets
            let mut severities_seen = 0u32;
            let mut flag_sets: u32 = 0; // simplified check

            // Simple disagreement check: if we have multiple responding attestors with
            // different severity or flags, it's Contested
            if unique_attestors.len() > 1 && responding_count > 1 {
                // Check if all responding have the same severity and flags
                let mut first_severity: Option<u32> = None;
                let mut first_flags: Option<u32> = None;
                let mut has_difference = false;

                for attestor in &unique_attestors {
                    let key = DataKey::Attestation(attestor.clone(), asset.clone());
                    if let Some(safety) = env.storage().persistent().get(&key) {
                        if now.saturating_sub(safety.attested_at) <= max_age_secs {
                            if first_severity.is_none() {
                                first_severity = Some(safety.severity);
                                first_flags = Some(safety.flags);
                            } else {
                                if safety.severity != first_severity.unwrap() {
                                    has_difference = true;
                                }
                                if safety.flags != first_flags.unwrap() {
                                    has_difference = true;
                                }
                            }
                        }
                    }
                    if has_difference {
                        break;
                    }
                }

                if has_difference {
                    // Contested: return the conservative result alongside the count
                    // We need to return both the Quorum variant and the count
                    // Since Quorum::Contested(u32) only carries the count,
                    // we compute the conservative result separately
                    // For measurement purposes, we'll return Contested with the count
                    // and the caller can compute the conservative result
                    return Quorum::Contested(responding_count);
                }
            }

            Quorum::Met(result_safety)
        }
    }

    /// Checks if an asset is safe according to the quorum, using conservative aggregation.
    pub fn is_safe_quorum(
        env: Env,
        asset: Address,
        attestors: Vec<Address>,
        threshold: u32,
        max_severity: u32,
        max_age_secs: u64,
    ) -> bool {
        match Self::quorum(env.clone(), asset, attestors.clone(), threshold, max_age_secs) {
            Quorum::Met(safety) => {
                // Check that the conservative result is within the allowed severity
                safety.severity <= max_severity
            }
            Quorum::Unavailable(_) => false,
            Quorum::Contested(_) => {
                // Contested: fail closed unless the conservative result is within policy
                // For safety, we fail closed on contested
                false
            }
        }
    }

    /// Deduplicates a list of attestors, preserving order.
    fn deduplicate_attestors(attestors: &Vec<Address>) -> Vec<Address> {
        let mut seen: Vec<Address> = Vec::new(&env);
        let mut unique = Vec::new(&env);
        for attestor in attestors.iter() {
            if !seen.contains(attestor) {
                seen.push_back(attestor.clone());
                unique.push_back(attestor.clone());
            }
        }
        unique
    }
}

#[cfg(test)]
mod test {
    use super::*;
    use soroban_sdk::Env;

    fn make_safety(env: &Env, severity: u32, flags: u32, attested_at: u64) -> Safety {
        Safety {
            severity,
            flags,
            evidence_hash: [0u8; 32],
            attested_at,
        }
    }

    #[test]
    fn test_quorum_single_attestor() {
        let env = Env::default();
        let contract = quorum_measurement::Contract::new(&env);

        let attestor = Address::generate(&env);
        let asset = Address::generate(&env);

        // Write a fresh attestation
        contract.attest(&env, attestor.clone(), asset.clone(), SEVERITY_MEDIUM, MECH_AUTH_REQUIRED);

        // Read quorum with threshold=1
        let result = contract.quorum(
            &env,
            asset.clone(),
            vec![attestor.clone()],
            1,
            24 * 60 * 60, // 1 day freshness
        );

        assert_eq!(result, Quorum::Met(Safety {
            severity: SEVERITY_MEDIUM,
            flags: MECH_AUTH_REQUIRED,
            evidence_hash: [0u8; 32],
            attested_at: 0,
        }));
    }

    #[test]
    fn test_quorum_multiple_attestors_same() {
        let env = Env::default();
        let contract = quorum_measurement::Contract::new(&env);

        let attestor1 = Address::generate(&env);
        let attestor2 = Address::generate(&env);
        let asset = Address::generate(&env);

        // Write same attestation from both attestors
        contract.attest(&env, attestor1.clone(), asset.clone(), SEVERITY_MEDIUM, MECH_AUTH_REQUIRED);
        contract.attest(&env, attestor2.clone(), asset.clone(), SEVERITY_MEDIUM, MECH_AUTH_REQUIRED);

        // Read quorum with threshold=2
        let result = contract.quorum(
            &env,
            asset.clone(),
            vec![attestor1.clone(), attestor2.clone()],
            2,
            24 * 60 * 60,
        );

        // Same attestations -> not contested, conservative result
        assert_eq!(result, Quorum::Met(Safety {
            severity: SEVERITY_MEDIUM,
            flags: MECH_AUTH_REQUIRED,
            evidence_hash: [0u8; 32],
            attested_at: 0,
        }));
    }

    #[test]
    fn test_quorum_contested() {
        let env = Env::default();
        let contract = quorum_measurement::Contract::new(&env);

        let attestor1 = Address::generate(&env);
        let attestor2 = Address::generate(&env);
        let asset = Address::generate(&env);

        // Write different severities from each attestor
        contract.attest(&env, attestor1.clone(), asset.clone(), SEVERITY_MEDIUM, MECH_AUTH_REQUIRED);
        contract.attest(&env, attestor2.clone(), asset.clone(), SEVERITY_HIGH, 0);

        // Read quorum with threshold=2
        let result = contract.quorum(
            &env,
            asset.clone(),
            vec![attestor1.clone(), attestor2.clone()],
            2,
            24 * 60 * 60,
        );

        // Different severities -> Contested
        assert_eq!(result, Quorum::Contested(2));
    }

    #[test]
    #[should_panic(expected = "Quorum: threshold must be > 0")]
    fn test_quorum_zero_threshold() {
        let env = Env::default();
        let contract = quorum_measurement::Contract::new(&env);
        let asset = Address::generate(&env);
        contract.quorum(&env, asset, vec![], 0, 24 * 60 * 60);
    }

    #[test]
    #[should_panic(expected = "Quorum: threshold exceeds number of unique attestors")]
    fn test_quorum_threshold_too_high() {
        let env = Env::default();
        let contract = quorum_measurement::Contract::new(&env);
        let asset = Address::generate(&env);
        contract.quorum(&env, asset, vec![Address::generate(&env)], 5, 24 * 60 * 60);
    }
}