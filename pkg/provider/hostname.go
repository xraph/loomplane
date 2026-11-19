package provider

import "strings"

// Hostname turns a person's server name into a hostname a provider accepts:
// letters, digits and hyphens in one DNS label of at most 63 bytes. A name
// that is already a valid hostname is kept as written, dots and all, so an
// adapter that needs a hostname calls this rather than refusing the name.
// "Digital Ocean Droplet 1" becomes "Digital-Ocean-Droplet-1".
func Hostname(name string) string {
	if ValidHostname(name) {
		return name
	}
	var b strings.Builder
	hyphen := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		if isHostnameAlnum(c) {
			b.WriteByte(c)
			hyphen = false
		} else if b.Len() > 0 && !hyphen {
			b.WriteByte('-')
			hyphen = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	if out == "" {
		return "loomplane-server"
	}
	return out
}

// ValidHostname reports whether name is an RFC 1123 hostname: dot-separated
// labels of letters, digits and inner hyphens, each at most 63 bytes.
func ValidHostname(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			if c := label[i]; !isHostnameAlnum(c) && c != '-' {
				return false
			}
		}
	}
	return true
}

func isHostnameAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
