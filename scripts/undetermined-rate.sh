#!/usr/bin/env bash
# Scan attested assets every 6 hours for a week and record the undetermined rate per source.
set -u
set -o pipefail

ASSETS_FILE=""
OUTPUT_DIR=""
RUNS=168
DELAY=""

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
    echo "usage: $0 [--assets FILE] [--output DIR] [--runs N] [--delay SECS] [--help]" >&2
}

while [ $# -gt 0 ]; do
    case "$1" in
        --assets) ASSETS_FILE="$2"; shift 2 ;;
        --output) OUTPUT_DIR="$2"; shift 2 ;;
        --runs) RUNS="$2"; shift 2 ;;
        --delay) DELAY="$2"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "reproducibility: unknown argument $1" >&2; usage; exit 1 ;;
    esac
done

if [ -z "$ASSETS_FILE" ]; then
    ASSETS_FILE="$SCRIPT_DIR/assets.tsv"
    if [ ! -f "$ASSETS_FILE" ]; then
        cat > "$ASSETS_FILE" << 'TSV'
USDC-GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN
USDZ-GAKTLPC4ZV37SSCITQ5IS5AQ4WPF4CF4VZJQPPAROSGXMYOATF5U6XPR
BERKSHIRE-GA22QHSHQEHDJS2ZOINSC77XPPQ24G5EFRJGVEIZLKC5FAW3PQ5XNSDQ
ARST-GCSAZVWXZKWS4XS223M5F54H2B6XPIIXZZGP7KEAIU6YSL5HDRGCI3DG
USDGLO-GBBS25EGYQPGEZCGCFBKG4OAGFXU6DSOQBGTHELLJT3HZXZJ34HWS6XV
DOGE-GA22IDJNHUMC3XKUCCBFNTQIJOUBWINC5GCXHLJ2V6KZ3OWAXCULNQ7P
VELO-GDM4RQUQQUVSKQA7S6EM7XBZP3FCGH4Q7CL6TABQ7B2BEJ5ERARM2M5M
REPO-GA2BVQLGAG6UJDPHHTJDQAPBUYJV7D6IWIISGJI2LMQXTOKMHO36EAZR
KALE-GAKJM27QTNLBBZ352HQ4IDR3GWXUXQUEBKBDWOJG7RBH2NVEWUNSYCEF
AQUA-GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA
TSV
    fi
fi

if [ -z "$OUTPUT_DIR" ]; then
    OUTPUT_DIR="$SCRIPT_DIR/undetermined-rates"
fi

mkdir -p "$OUTPUT_DIR"

ASSET_COUNT="$(wc -l < "$ASSETS_FILE" | tr -d ' ')"
echo "scanning $ASSET_COUNT assets over $RUNS runs (6h intervals)" >&2
echo "output directory: $OUTPUT_DIR" >&2

# Create assets list for the loop
ASSETS_LIST="$(cat "$ASSETS_FILE")"

# Track source counts using a simple approach: accumulate raw JSON
SOURCE_TRACKING="$OUTPUT_DIR/.source-tracking.jsonl"
: > "$SOURCE_TRACKING"

# Seconds between run starts: --delay, or 6h (21600s) by default.
INTERVAL="${DELAY:-21600}"

run_num=1
start_time="$(date -u +%s)"

while [ "$run_num" -le "$RUNS" ]; do
    run_start="$(date -u +%s)"
    run_label="$(printf 'run-%03d' "$run_num")"
    echo "=== Run $run_num/$RUNS at $(date -u) ===" >&2

    # Process each asset
    for asset in $ASSETS_LIST; do
        [ -n "$asset" ] || continue

        # Run a full scan to get undetermined data
        report_file="$OUTPUT_DIR/tmp-report-$asset.json"
        if command -v timeout >/dev/null 2>&1; then
            out="$(timeout 120 ./assay scan "$asset" 2>/dev/null)" || true
        else
            out="$(./assay scan "$asset" 2>/dev/null)" || true
        fi

        if [ -z "$out" ]; then
            echo "  $asset: scan produced no output" >&2
            # Write empty result
            echo '{"asset":"'$asset'","undetermined":false,"undetermined_checks":[],"undetermined_by_source":{},"undetermined_rate":"0/0"}' > "$report_file"
        else
            # Write the full report to a temp file for python to process
            echo "$out" > "$report_file"
            # Use python to extract undetermined fields
            python3 -c "
import json, sys
with open('$report_file') as f:
    d = json.load(f)
by_source = d.get('undetermined_by_source', {})
rate = d.get('undetermined_rate', '0/0')
checks = d.get('undetermined_checks', [])
undetermined = d.get('undetermined', False)
# Write extracted data as JSON line
print(json.dumps({
    'asset': '$asset',
    'undetermined': undetermined,
    'undetermined_checks': checks,
    'undetermined_by_source': by_source,
    'undetermined_rate': rate
}))
" > "$OUTPUT_DIR/$run_label-$asset-extracted.json"
            # Append to tracking file
            cat "$OUTPUT_DIR/$run_label-$asset-extracted.json" >> "$SOURCE_TRACKING"
        fi

    done

    run_end="$(date -u +%s)"
    run_elapsed=$((run_end - run_start))
    echo "Run $run_num/$RUNS completed in ${run_elapsed}s" >&2

    # Calculate when the next run should start (INTERVAL after this one started)
    if [ "$run_num" -lt "$RUNS" ]; then
        desired_next="$((run_start + INTERVAL))"
        now="$(date -u +%s)"
        sleep_time=$((desired_next - now))
        if [ "$sleep_time" -gt 0 ]; then
            echo "  sleeping ${sleep_time}s until next run" >&2
            sleep "$sleep_time"
        fi
    fi

    run_num=$((run_num + 1))
done

# Generate summary using python
python3 << 'PYEOF'
import json, os
from collections import defaultdict

summary_dir = os.environ.get("OUTPUT_DIR", "./undetermined-rates")
source_counts = defaultdict(int)

# Read all extracted data files
for fname in sorted(os.listdir(summary_dir)):
    if not fname.startswith("run-") or not fname.endswith("-extracted.json"):
        continue
    if fname == ".source-tracking.jsonl":
        continue
    fpath = os.path.join(summary_dir, fname)
    with open(fpath, "r") as f:
        try:
            data = json.load(f)
        except json.JSONDecodeError:
            continue
    by_source = data.get("undetermined_by_source", {})
    if isinstance(by_source, dict):
        for source, count in by_source.items():
            source_counts[source] += count

# Calculate total runs
total_runs = 0
for fname in os.listdir(summary_dir):
    if fname.startswith("run-") and fname.endswith("-extracted.json") and fname != ".source-tracking.jsonl":
        total_runs += 1

# Generate output
lines = []
lines.append("# Undetermined Rate Analysis")
lines.append(f"* Scanned {total_runs} asset-runs across {len(source_counts)} sources *")
lines.append("")
lines.append("## Rate per source")
lines.append("| Source | Count | Rate (%) |")
lines.append("|--------|-------|----------|")

for source in sorted(source_counts.keys(), key=lambda s: source_counts[s], reverse=True):
    count = source_counts[source]
    rate_pct = round(100 * count / (total_runs * 10)) if total_runs > 0 else 0
    lines.append(f"| {source} | {count} | {rate_pct} |")

lines.append("")
lines.append("## Implications for writer cadence")

# Determine implications based on data
high_rate_sources = [s for s, c in source_counts.items() if c > total_runs * 0.3] if total_runs > 0 else []
if high_rate_sources:
    lines.append("- **High undetermined rates detected** on sources: " + ", ".join(high_rate_sources))
    lines.append(f"  - With {sum(source_counts[s] for s in high_rate_sources)} undetermined checks out of {total_runs * 10} total")
    lines.append(f"  - **Scheduled re-attestation will largely refuse** when these sources are down,")
    lines.append(f"    meaning the writer cadence must account for frequent undetermined outcomes.")
else:
    lines.append("- **Low undetermined rates** across all sources")
    lines.append("  - Scheduled re-attestation should succeed most of the time")
    lines.append("  - Writer cadence can be more aggressive without excessive refusals.")

lines.append("")
lines.append(f"*Data collected over {total_runs} scans of {len(source_counts)} sources every 6 hours*")

summary_path = os.path.join(summary_dir, "summary.json")
with open(summary_path, "w") as f:
    f.write("\n".join(lines))

print(f"Summary written to {summary_path}")
PYEOF

end_time="$(date -u +%s)"
total_duration=$((end_time - start_time))
echo "=== Complete ===" >&2
echo "Ran $RUNS scans over $((total_duration / 60))m" >&2
echo "Summary: $OUTPUT_DIR/summary.json" >&2
exit 0