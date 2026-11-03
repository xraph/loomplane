// Package install renders node.sh, the script that installs loomctl on a
// machine and joins it to Loomplane.
package install

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"text/template"
)

//go:embed node.sh.tmpl
var nodeSh string

var tmpl = template.Must(template.New("node.sh").Parse(nodeSh))

// Params are the only things an instance puts into the script.
type Params struct {
	// PublicURL is where this Loomplane is, such as https://loomplane.example.com.
	PublicURL string
	// Version is the release this Loomplane runs, such as v1.8.0.
	Version string
	// Identity (the tag follows it), MainIdentity and Issuer are what the
	// release signature must name. node.sh carries them as text.
	Identity     string
	MainIdentity string
	Issuer       string
	// Sums is the sha256 of each loomctl archive this Loomplane has verified
	// against the release signature, by platform (linux_amd64). Empty when
	// it has not, and the script then refuses to install without cosign.
	Sums map[string]string
}

// platforms node.sh knows.
var platforms = map[string]bool{"linux_amd64": true, "linux_arm64": true, "darwin_amd64": true, "darwin_arm64": true}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

type sum struct{ Platform, Sum string }

// version is a release tag: letters, digits and . + _ - only.
var version = regexp.MustCompile(`^v?\d[0-9A-Za-z.+_-]*$`)

// shQuote single-quotes s for sh. It refuses what cannot be made safe inside
// single quotes: a quote, or any control character, newlines included.
func shQuote(what, s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("%s is empty", what)
	}
	for _, r := range s {
		if r == '\'' || r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%s contains a character that cannot go into the script", what)
		}
	}
	return "'" + s + "'", nil
}

// Render is the script for p. It fails when a value would need escaping
// beyond single quotes, so a hostile setting cannot add a command.
func Render(p Params) (string, error) {
	u := strings.TrimRight(p.PublicURL, "/")
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return "", errors.New("the public URL must start with http:// or https://")
	}
	if strings.ContainsAny(u, " \t\r\n\\$`\"") {
		return "", errors.New("the public URL contains a character that cannot go into the script")
	}
	if !version.MatchString(p.Version) {
		return "", fmt.Errorf("the version %q is not a release tag", p.Version)
	}
	var d struct {
		URLComment, PublicURL, Version, Identity, MainIdentity, Issuer string
		Sums                                                           []sum
	}
	var err error
	d.URLComment = u
	if d.PublicURL, err = shQuote("the public URL", u); err != nil {
		return "", err
	}
	if d.Version, err = shQuote("the version", p.Version); err != nil {
		return "", err
	}
	if d.Identity, err = shQuote("the identity", p.Identity); err != nil {
		return "", err
	}
	if d.MainIdentity, err = shQuote("the main identity", p.MainIdentity); err != nil {
		return "", err
	}
	if d.Issuer, err = shQuote("the issuer", p.Issuer); err != nil {
		return "", err
	}
	keys := make([]string, 0, len(p.Sums))
	for k := range p.Sums {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if !platforms[k] || !sha256Hex.MatchString(p.Sums[k]) {
			return "", fmt.Errorf("the sum for %q is not a platform and a lower-case sha256", k)
		}
		d.Sums = append(d.Sums, sum{k, "'" + p.Sums[k] + "'"})
	}
	var b strings.Builder
	if xerr := tmpl.Execute(&b, d); xerr != nil {
		return "", xerr
	}
	return b.String(), nil
}
