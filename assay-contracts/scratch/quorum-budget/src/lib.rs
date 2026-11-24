#![no_std]

//! Scratch-only Scheme C quorum prototype used to measure N independent
//! persistent `(attestor, asset)` reads. This crate is excluded from the
//! deployable workspace and is not part of the registry ABI.

#[cfg(test)]
extern crate std;

use soroban_sdk::{contract, contractimpl, contracttype, Address, BytesN, Env, Vec};

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Safety {
    pub severity: u32,
    pub flags: u32,
    pub evidence_hash: BytesN<32>,
    pub attested_at: u64,
}

#[contracttype]
#[derive(Clone)]
enum DataKey {
    Attestation(Address, Address),
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct QuorumResult {
    pub responding: u32,
    pub severity: u32,
    pub flags: u32,
    pub contested: bool,
}

#[contract]
pub struct QuorumBudget;

#[contractimpl]
impl QuorumBudget {
    /// Seeds benchmark data. This write path is excluded from the read cost.
    pub fn seed(env: Env, attestor: Address, asset: Address, safety: Safety) {
        env.storage()
            .persistent()
            .set(&DataKey::Attestation(attestor, asset), &safety);
    }

    /// Reads each named attestor once, ignores absent and stale entries, and
    /// returns the conservative max-severity / union-of-flags aggregate.
    pub fn quorum(
        env: Env,
        asset: Address,
        attestors: Vec<Address>,
        max_age_secs: u64,
    ) -> QuorumResult {
        let now = env.ledger().timestamp();
        let mut result = QuorumResult {
            responding: 0,
            severity: 0,
            flags: 0,
            contested: false,
        };
        let mut first: Option<(u32, u32)> = None;

        for attestor in attestors.iter() {
            let key = DataKey::Attestation(attestor, asset.clone());
            let Some(safety) = env.storage().persistent().get::<_, Safety>(&key) else {
                continue;
            };
            if max_age_secs != 0 && now.saturating_sub(safety.attested_at) > max_age_secs {
                continue;
            }
            result.responding += 1;
            result.severity = result.severity.max(safety.severity);
            result.flags |= safety.flags;
            match first {
                Some((severity, flags)) if (severity, flags) != (safety.severity, safety.flags) => {
                    result.contested = true;
                }
                None => first = Some((safety.severity, safety.flags)),
                _ => {}
            }
        }
        result
    }
}

#[cfg(test)]
mod test {
    use super::*;
    use soroban_sdk::testutils::{Address as _, Ledger as _};

    const SIZES: [u32; 5] = [1, 2, 4, 8, 16];

    #[test]
    fn print_quorum_budget_by_attestor_count() {
        let env = Env::default();
        let contract_id = env.register(QuorumBudget, ());
        let client = QuorumBudgetClient::new(&env, &contract_id);
        env.ledger().set_timestamp(10_000);
        let asset = Address::generate(&env);
        let mut prepared = Vec::new(&env);

        // Seed outside the invocation being measured. Increment the severity
        // and flags so aggregation work is exercised as well as storage reads.
        for index in 0..*SIZES.last().unwrap() {
            let attestor = Address::generate(&env);
            client.seed(
                &attestor,
                &asset,
                &Safety {
                    severity: index % 5,
                    flags: 1 << (index % 6),
                    evidence_hash: BytesN::from_array(&env, &[index as u8; 32]),
                    attested_at: 10_000,
                },
            );
            prepared.push_back(attestor);
        }

        std::println!("N,instructions,memory_bytes,disk_reads,memory_reads,read_entries");
        for n in SIZES {
            let mut attestors = Vec::new(&env);
            for i in 0..n {
                attestors.push_back(prepared.get(i).unwrap());
            }
            let result = client.quorum(&asset, &attestors, &3600);
            assert_eq!(result.responding, n);
            assert_eq!(result.severity, (n - 1).min(4));
            assert_eq!(result.flags, (1u32 << n.min(6)) - 1);
            assert_eq!(result.contested, n > 1);
            let r = env.cost_estimate().resources();
            std::println!(
                "{n},{},{},{},{},{}",
                r.instructions,
                r.mem_bytes,
                r.disk_read_entries,
                r.memory_read_entries,
                r.disk_read_entries + r.memory_read_entries,
                // The final column is deliberately storage-read entries only;
                // ledger footprint write entries are zero on this read path.
            );
        }
    }
}
