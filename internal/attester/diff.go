package attester

import (
	"bufio"
	"fmt"
	"sort"
	"strings"
)

// Line is one parsed dry-run output line (see FormatResult).
type Line struct {
	Asset    string
	SAC      string
	Status   Status
	Severity string // attest only
	Flags    string // attest only
	Hash     string // attest only
	Reason   string // refused/error only
}

// ParseOutput parses dry-run output into one Line per asset. Blank lines are
// ignored; a malformed line is an error so a truncated output cannot diff as
// "asset removed".
func ParseOutput(text string) (map[string]Line, error) {
	out := map[string]Line{}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		raw := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		f := strings.Split(raw, "\t")
		var l Line
		switch {
		case len(f) == 5:
			l = Line{Asset: f[0], SAC: f[1], Status: StatusAttest, Severity: f[2], Flags: f[3], Hash: f[4]}
		case len(f) == 4 && (f[2] == string(StatusRefused) || f[2] == string(StatusError)):
			l = Line{Asset: f[0], SAC: f[1], Status: Status(f[2]), Reason: f[3]}
		default:
			return nil, fmt.Errorf("line %d: not a dry-run result: %q", n, raw)
		}
		if _, dup := out[l.Asset]; dup {
			return nil, fmt.Errorf("line %d: %s appears twice", n, l.Asset)
		}
		out[l.Asset] = l
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read dry-run output: %w", err)
	}
	return out, nil
}

// FieldChange is one field of one asset that differs between two outputs.
type FieldChange struct {
	Asset string
	Field string
	Base  string
	Head  string
}

// Diff compares a base and a head dry-run output, field by field, in asset
// order. An asset present on one side only is reported as a single "asset"
// change.
func Diff(base, head map[string]Line) []FieldChange {
	assets := make([]string, 0, len(base)+len(head))
	for a := range base {
		assets = append(assets, a)
	}
	for a := range head {
		if _, ok := base[a]; !ok {
			assets = append(assets, a)
		}
	}
	sort.Strings(assets)

	var changes []FieldChange
	for _, a := range assets {
		b, inBase := base[a]
		h, inHead := head[a]
		switch {
		case !inBase:
			changes = append(changes, FieldChange{a, "asset", "(absent)", summary(h)})
		case !inHead:
			changes = append(changes, FieldChange{a, "asset", summary(b), "(absent)"})
		default:
			for _, f := range []struct{ name, b, h string }{
				{"status", string(b.Status), string(h.Status)},
				{"severity", b.Severity, h.Severity},
				{"flags", b.Flags, h.Flags},
				{"evidence_hash", b.Hash, h.Hash},
				{"reason", b.Reason, h.Reason},
				{"sac", b.SAC, h.SAC},
			} {
				if f.b != f.h {
					changes = append(changes, FieldChange{a, f.name, orDash(f.b), orDash(f.h)})
				}
			}
		}
	}
	return changes
}

func summary(l Line) string {
	if l.Status == StatusAttest {
		return fmt.Sprintf("attest severity=%s flags=%s", l.Severity, l.Flags)
	}
	return string(l.Status)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// RenderMarkdown renders a diff as a GitHub job summary. baseNote, when not
// empty, replaces the comparison with an explanation (for example, the base
// commit has no attester to run).
func RenderMarkdown(changes []FieldChange, head map[string]Line, baseNote string) string {
	var b strings.Builder
	b.WriteString("### Attester dry-run: derived attestation parameters\n\n")
	b.WriteString("Offline, from committed fixtures (`.github/attester/fixtures.json`). ")
	b.WriteString("A change here means this PR changes what would be written on-chain for that asset.\n\n")

	if baseNote != "" {
		b.WriteString("**No comparison:** " + baseNote + "\n\n")
	} else if len(changes) == 0 {
		fmt.Fprintf(&b, "**No change** to severity, flags or evidence hash for any of the %d assets.\n\n", len(head))
	} else {
		fmt.Fprintf(&b, "**%d field(s) changed:**\n\n", len(changes))
		b.WriteString("| Asset | Field | Base | Head |\n| --- | --- | --- | --- |\n")
		for _, c := range changes {
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", c.Asset, c.Field, cell(c.Base), cell(c.Head))
		}
		b.WriteString("\n")
	}

	b.WriteString("<details><summary>Head dry-run, per asset</summary>\n\n")
	b.WriteString("| Asset | Status | Severity | Flags | Evidence hash / reason |\n| --- | --- | --- | --- | --- |\n")
	assets := make([]string, 0, len(head))
	for a := range head {
		assets = append(assets, a)
	}
	sort.Strings(assets)
	for _, a := range assets {
		l := head[a]
		detail := l.Hash
		if l.Status != StatusAttest {
			detail = l.Reason
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n", a, l.Status, orDash(l.Severity), orDash(l.Flags), cell(detail))
	}
	b.WriteString("\n</details>\n")
	return b.String()
}

// cell keeps a value from breaking the Markdown table it is rendered into.
func cell(s string) string {
	return "`" + strings.NewReplacer("|", `\|`, "`", "'").Replace(s) + "`"
}
