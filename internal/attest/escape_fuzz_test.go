package attest

import (
	"fmt"
	"strings"
	"testing"
)

// FuzzEscape pins the preimage escaping from both directions (#289).
//
// Claims embed third-party text, so escape is security-relevant (threat model
// class 6): an unescaped tab or newline lets an issuer forge a preimage line
// and attribute a claim to a source that never made it. The example tests pin
// the escapes they thought of; the fuzzer pins the function against every
// string it can build.
//
// Two properties, exactly as the issue asks:
//
//   - An escaped field can never contain a raw tab, LF or CR — every separator
//     that could break out of a field comes out as an escape sequence.
//   - escape is injective. It is asserted directly (two different strings must
//     never escape to the same value, or two different claims would hash to
//     the same preimage), and again through a round trip: unescapeField
//     reverses escape on every input, and a left inverse exists only for an
//     injective function.
//
// No new dependency: standard-library testing.F only. Seeds come from the
// `escapes-in-claim` vector and the two injection tests.
func FuzzEscape(f *testing.F) {
	// The escapes-in-claim vector's raw claim: every escapable separator and a
	// backslash, in one field.
	f.Add(
		"issuer flags: auth_required=false\nsecond\tline\\with\\backslash\rend",
		"issuer flags: auth_required=false\nsecond\tline\\with\\backslash\rend",
	)
	// The hostile directory name and the forged evidence line from the
	// injection tests: the strings that must not become a preimage line.
	f.Add("Evil\tName\nSecond\rLine\\Path\tand\\tseq", "evidence\thorizon\thttps://evil\tissuer flags: none")
	// Degenerate and boundary pairs.
	f.Add("", "\t\n\r\\")
	f.Add("\\t", "\t") // literal backslash-t and a real tab must not collide
	f.Add("\\", "\\\\")
	f.Add("a", "b")

	f.Fuzz(func(t *testing.T, a, b string) {
		for _, s := range []string{a, b} {
			escaped := escape(s)
			// An escaped field can never contain a raw separator.
			if strings.ContainsAny(escaped, "\t\n\r") {
				t.Fatalf("escape(%q) = %q, which still carries a raw tab, LF or CR", s, escaped)
			}
			// Round trip: a left inverse of escape exists only if escape is
			// injective, so this re-proves the second property on every input
			// the fuzzer builds.
			got, err := unescapeField(escaped)
			if err != nil {
				t.Fatalf("escape(%q) = %q, which does not decode: %v", s, escaped, err)
			}
			if got != s {
				t.Fatalf("escape/unescape round trip of %q returned %q", s, got)
			}
		}
		// Injectivity: two different fields must never escape to the same
		// bytes, or two different claims could share a preimage.
		if ea, eb := escape(a), escape(b); ea == eb && a != b {
			t.Fatalf("escape is not injective: %q and %q both escape to %q", a, b, ea)
		}
	})
}

// unescapeField reverses escape, decoding the four escape sequences it emits.
// It is the fuzz target's independent reading of the escaped form: if it
// cannot invert escape, or inverts it to a different string, the encoding is
// not injective.
func unescapeField(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i == len(s) {
			return "", fmt.Errorf("dangling backslash at end of field")
		}
		switch s[i] {
		case '\\':
			b.WriteByte('\\')
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		default:
			return "", fmt.Errorf("unknown escape \\%c", s[i])
		}
	}
	return b.String(), nil
}
