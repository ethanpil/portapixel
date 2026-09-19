package slug

import "strings"

// Make turns a name into lower case letters, digits and hyphens. Every other
// character becomes a hyphen, a run of hyphens becomes one hyphen, and the
// hyphens at the two ends go away. The result is safe on exFAT, safe in a URL
// and safe in a shell, because a person reads this card on a laptop.
//
// The result can be empty. The caller decides what an empty name means.
func Make(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}
