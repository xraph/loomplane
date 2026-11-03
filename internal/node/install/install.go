// Package install renders node.sh, the script that installs loomctl on a
// machine and joins it to Loomplane.
package install

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

//go:embed node.sh.tmpl
var nodeSh string

var tmpl = template.Must(template.New("node.sh").Parse(nodeSh))

const (
	// ReleaseIdentity is the start of the identity that signs a Loomplane
	// release: the Release workflow, run on a tag. The tag follows it.
	ReleaseIdentity = "https://github.com/xraph/loomplane/.github/workflows/release.yml@refs/tags/"
	// ReleaseIssuer is the OIDC issuer of GitHub Actions, which vouches for
	// that identity.
	ReleaseIssuer = "https://token.actions.githubusercontent.com"
)

// Params are the only things an instance puts into the script.
type Params struct {
	// PublicURL is where this Loomplane is, such as https://loomplane.example.com.
	PublicURL string
	// Version is the release this Loomplane runs, such as v1.8.0.
	Version string
	// Identity and Issuer are what the release signature must name. They are
	// fixed by the release workflow, so node.sh carries them as text.
	Identity string
	Issuer   string
}

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
	var d struct{ URLComment, PublicURL, Version, Identity, Issuer string }
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
	if d.Issuer, err = shQuote("the issuer", p.Issuer); err != nil {
		return "", err
	}
	var b strings.Builder
	if xerr := tmpl.Execute(&b, d); xerr != nil {
		return "", xerr
	}
	return b.String(), nil
}
