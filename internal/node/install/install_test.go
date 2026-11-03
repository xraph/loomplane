package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func params() Params {
	return Params{PublicURL: "https://loom.example.com", Version: "v1.8.0",
		Identity:     "https://github.com/xraph/loomplane/.github/workflows/release.yml@refs/tags/",
		MainIdentity: "https://github.com/xraph/loomplane/.github/workflows/release.yml@refs/heads/main", Issuer: "https://token.actions.githubusercontent.com"}
}

func render(t *testing.T) string {
	t.Helper()
	s, err := Render(params())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRenderRefusesWhatSingleQuotesCannotHold(t *testing.T) {
	for name, mut := range map[string]func(*Params){
		"quote in url":       func(p *Params) { p.PublicURL = "https://a.example/'x" },
		"command in url":     func(p *Params) { p.PublicURL = "https://a.example/$(id)" },
		"backtick in url":    func(p *Params) { p.PublicURL = "https://a.example/`id`" },
		"space in url":       func(p *Params) { p.PublicURL = "https://a.example/ x" },
		"newline in url":     func(p *Params) { p.PublicURL = "https://a.example/\nid" },
		"no scheme":          func(p *Params) { p.PublicURL = "a.example" },
		"other scheme":       func(p *Params) { p.PublicURL = "file:///etc/passwd" },
		"quote in version":   func(p *Params) { p.Version = "v1'" },
		"space in version":   func(p *Params) { p.Version = "v1 2" },
		"semicolon":          func(p *Params) { p.Version = "v1;id" },
		"empty version":      func(p *Params) { p.Version = "" },
		"quote in identity":  func(p *Params) { p.Identity = "x'y" },
		"newline in issuer":  func(p *Params) { p.Issuer = "x\ny" },
		"version not a tag":  func(p *Params) { p.Version = "latest" },
		"version with slash": func(p *Params) { p.Version = "v1/../x" },
	} {
		p := params()
		mut(&p)
		if s, err := Render(p); err == nil {
			t.Errorf("%s: rendered %q", name, s)
		}
	}
}

func TestRenderAcceptsPrereleaseTagsAndPorts(t *testing.T) {
	p := params()
	p.PublicURL, p.Version = "http://localhost:8780/", "v1.3.0-rc.1"
	s, err := Render(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "LOOMPLANE_URL='http://localhost:8780'\n") || !strings.Contains(s, "LOOMPLANE_VERSION='v1.3.0-rc.1'\n") {
		t.Fatalf("script:\n%s", s)
	}
}

func TestScriptHasNoEmDash(t *testing.T) {
	if s := render(t); strings.ContainsRune(s, '\u2014') {
		t.Fatal("em dash in node.sh")
	}
}

func TestRenderPutsVerifiedSumsInTheScript(t *testing.T) {
	p := params()
	sum := strings.Repeat("ab", 32)
	p.Sums = map[string]string{"linux_amd64": sum, "darwin_arm64": strings.Repeat("cd", 32)}
	s, err := Render(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "linux_amd64) want_sum='"+sum+"' ;;") || !strings.Contains(s, "darwin_arm64) want_sum='"+strings.Repeat("cd", 32)+"' ;;") {
		t.Fatalf("sums missing:\n%s", s)
	}
	if without := render(t); strings.Contains(without, "want_sum='") {
		t.Fatal("a script rendered without verified sums carries one")
	}
	for name, sums := range map[string]map[string]string{
		"unknown platform": {"windows_amd64": sum}, "short sum": {"linux_amd64": "abc"}, "upper case": {"linux_amd64": strings.ToUpper(sum)},
		"injection": {"linux_amd64": sum[:60] + "';id"},
	} {
		p.Sums = sums
		if out, err := Render(p); err == nil {
			t.Errorf("%s: rendered %q", name, out)
		}
	}
}

func TestScriptParsesAndPassesShellcheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.sh")
	if err := os.WriteFile(path, []byte(render(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, sh := range []string{"sh", "dash"} {
		if bin, err := exec.LookPath(sh); err == nil {
			if out, err := exec.CommandContext(t.Context(), bin, "-n", path).CombinedOutput(); err != nil {
				t.Errorf("%s -n: %v\n%s", sh, err, out)
			}
		}
	}
	bin, err := exec.LookPath("shellcheck")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("shellcheck is not installed, and CI must run it")
		}
		t.Skip("shellcheck is not installed")
	}
	if out, err := exec.CommandContext(t.Context(), bin, "--shell=sh", "--severity=style", path).CombinedOutput(); err != nil {
		t.Fatalf("shellcheck:\n%s", out)
	}
}
