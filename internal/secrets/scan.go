// Package secrets implements a small, conservative regex scanner that flags
// content patterns commonly associated with leaked credentials.
//
// Per DESIGN.md open question #3, the policy is *warn-only* — Scan never
// modifies its input. Callers decide whether to proceed with an LLM request
// based on the findings. The match text itself is deliberately NOT returned,
// only its kind and line — anything stronger risks the warning channel
// becoming the new exfiltration vector.
package secrets

import (
	"bytes"
	"regexp"
)

// Finding describes one match. Kind is a short stable identifier (used in
// status messages and in tests); Line is 1-based.
type Finding struct {
	Kind string
	Line int
}

// Scan returns every match across the built-in regex set. The result is
// deduplicated by (Kind, Line) so a single line with multiple regex hits
// surfaces only once.
func Scan(src []byte) []Finding {
	if len(src) == 0 {
		return nil
	}
	seen := make(map[Finding]struct{})
	var out []Finding
	for _, r := range rules {
		for _, m := range r.re.FindAllIndex(src, -1) {
			line := lineNumber(src, m[0])
			f := Finding{Kind: r.kind, Line: line}
			if _, ok := seen[f]; ok {
				continue
			}
			seen[f] = struct{}{}
			out = append(out, f)
		}
	}
	return out
}

type rule struct {
	kind string
	re   *regexp.Regexp
}

// rules is the built-in regex set — chosen to be high-precision (low false
// positive) rather than exhaustive. Patterns adapted from gitleaks's defaults.
// Adding a rule: prefer something with a distinctive prefix/structure over
// pure entropy heuristics, which generate noise on real code.
var rules = []rule{
	{kind: "aws-access-key", re: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{kind: "github-token", re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9_]{36,}\b`)},
	{kind: "google-api-key", re: regexp.MustCompile(`\bAIza[0-9A-Za-z\-_]{35}\b`)},
	{kind: "slack-token", re: regexp.MustCompile(`\bxox[abprso]-[0-9A-Za-z\-]+`)},
	{kind: "stripe-live-key", re: regexp.MustCompile(`\bsk_live_[0-9A-Za-z]{24,}\b`)},
	{kind: "pem-private-key", re: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
}

// lineNumber returns the 1-based line containing byte offset i. Counts \n in
// src[:i]; simple and fast for the source sizes we care about.
func lineNumber(src []byte, i int) int {
	if i > len(src) {
		i = len(src)
	}
	return bytes.Count(src[:i], []byte{'\n'}) + 1
}

// Summary renders findings as a short, status-bar-friendly string like
// "2 possible secrets (aws-access-key, pem-private-key)". Returns "" for an
// empty slice so callers can use it directly as a one-line warning.
func Summary(f []Finding) string {
	if len(f) == 0 {
		return ""
	}
	kinds := make(map[string]struct{})
	for _, x := range f {
		kinds[x.Kind] = struct{}{}
	}
	var list []string
	for k := range kinds {
		list = append(list, k)
	}
	// stable order for tests / display
	sortStrings(list)
	return pluralSecrets(len(f)) + " (" + joinComma(list) + ")"
}

func pluralSecrets(n int) string {
	if n == 1 {
		return "1 possible secret"
	}
	return itoa(n) + " possible secrets"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func joinComma(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	out := xs[0]
	for _, x := range xs[1:] {
		out += ", " + x
	}
	return out
}

// sortStrings is a tiny insertion sort — fine for the handful of kinds we'd
// ever surface, and avoids pulling in sort just for this.
func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j-1], xs[j] = xs[j], xs[j-1]
		}
	}
}
