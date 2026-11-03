package detect

import (
	"bytes"
	"context"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/xraph/loomplane/pkg/discovery"
)

// Env finds the environment variables an application reads: the ones its
// code asks for by name, and the ones an example file such as .env.example
// lists. It reports names, and example values only from example files and
// defaults written in the code.
type Env struct{}

// Name implements discovery.Detector.
func (Env) Name() string { return "env" }

// envRead is one way a language reads a variable. The first group is the
// name; a second, when the pattern has one, is a default the code falls
// back to.
type envRead struct {
	re *regexp.Regexp
	// upper keeps only upper-case names, for patterns that also match
	// things that aren't variables, such as Spring's ${server.port}.
	upper bool
}

const envName = `([A-Za-z_][A-Za-z0-9_]*)`

func reads(upper bool, patterns ...string) []envRead {
	out := make([]envRead, len(patterns))
	for i, p := range patterns {
		out[i] = envRead{re: regexp.MustCompile(strings.ReplaceAll(p, "NAME", envName)), upper: upper}
	}
	return out
}

var (
	goReads = append(reads(false,
		`os\.(?:Getenv|LookupEnv)\(\s*"NAME"`),
		// Struct tags read by caarlos0/env and envconfig.
		reads(true, `\benv(?:config)?:"NAME[,"](?:[^`+"`"+`]*envDefault:"([^"]*)")?`)...)
	jsReads = reads(false,
		`process\.env\.NAME`,
		`process\.env\[\s*['"]NAME['"]\s*\]`,
		`import\.meta\.env\.NAME`,
		`Deno\.env\.get\(\s*['"]NAME['"]`,
		`Bun\.env\.NAME`)
	pyReads = reads(false,
		`os\.(?:getenv|environ\.get)\(\s*['"]NAME['"](?:\s*,\s*['"]([^'"]*)['"])?`,
		`os\.environ\[\s*['"]NAME['"]\s*\]`)
	rustReads = reads(false,
		`env::var(?:_os)?\(\s*"NAME"`,
		`\b(?:option_)?env!\(\s*"NAME"`)
	jvmReads = reads(false,
		`System\.getenv\(\s*"NAME"`)
	springReads = reads(true,
		`\$\{NAME(?::([^}]*))?\}`)
	rubyReads = reads(false,
		`ENV\[\s*['"]NAME['"]\s*\]`,
		`ENV\.fetch\(\s*['"]NAME['"](?:\s*,\s*['"]([^'"]*)['"])?`)
	phpReads = reads(false,
		`getenv\(\s*['"]NAME['"]`,
		`\$_(?:ENV|SERVER)\[\s*['"]NAME['"]\s*\]`,
		`\benv\(\s*['"]NAME['"](?:\s*,\s*['"]([^'"]*)['"])?`)
)

// readsFor returns how a source file reads variables, by its name, or nil
// for a file no pattern applies to.
func readsFor(name string) []envRead {
	base := path.Base(name)
	switch path.Ext(base) {
	case ".go":
		return goReads
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".mts", ".cts", ".vue", ".svelte", ".astro":
		return jsReads
	case ".py":
		return pyReads
	case ".rs":
		return rustReads
	case ".java", ".kt", ".kts", ".scala", ".groovy":
		return jvmReads
	case ".rb":
		return rubyReads
	case ".php":
		return phpReads
	case ".properties", ".yml", ".yaml":
		if strings.HasPrefix(base, "application") || strings.HasPrefix(base, "bootstrap") {
			return springReads
		}
	}
	return nil
}

// envIgnored are names the operating system or a bundler sets, which no
// one deploying the application sets by hand.
var envIgnored = map[string]bool{
	"PATH": true, "HOME": true, "PWD": true, "USER": true, "SHELL": true, "TERM": true,
	"HOSTNAME": true, "TMPDIR": true, "LANG": true, "LC_ALL": true, "CI": true,
}

// viteBuiltins are what import.meta.env always has.
var viteBuiltins = map[string]bool{"MODE": true, "BASE_URL": true, "PROD": true, "DEV": true, "SSR": true}

// envFileKind says what a dotenv-style file is: an example meant to be
// committed, a real one that holds values, or neither.
func envFileKind(name string) (example, real bool) {
	base := strings.ToLower(path.Base(name))
	isEnv := base == ".env" || strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".env") || base == "env.example"
	if !isEnv {
		return false, false
	}
	for _, w := range []string{"example", "sample", "template", "dist", "defaults", "tpl"} {
		if strings.Contains(base, w) {
			return true, false
		}
	}
	return false, true
}

// testPath reports whether a file is a test, which reads variables a
// deployment never sets.
func testPath(name string) bool {
	for _, seg := range strings.Split(path.Dir(name), "/") {
		switch seg {
		case "test", "tests", "__tests__", "testdata", "e2e", "fixtures", "__mocks__":
			return true
		}
	}
	base := path.Base(name)
	return strings.HasSuffix(base, "_test.go") || strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") || strings.HasPrefix(base, "test_")
}

type envFound struct {
	v       *discovery.EnvUse
	files   map[string]bool
	fromEx  bool // Example came from an example file, which beats a code default
	certain bool
}

// Detect implements discovery.Detector.
func (Env) Detect(ctx context.Context, repo discovery.Repo, r *discovery.Report) error {
	found := map[string]*envFound{}
	add := func(name string, ev discovery.Evidence, example string, fromExample, certain bool) {
		f := found[name]
		if f == nil {
			f = &envFound{v: &discovery.EnvUse{Name: name}, files: map[string]bool{}}
			found[name] = f
		}
		if !f.files[ev.Path] {
			f.files[ev.Path] = true
			f.v.Evidence = append(f.v.Evidence, ev)
		}
		if example != "" && (f.v.Example == "" || (fromExample && !f.fromEx)) {
			f.v.Example, f.fromEx = example, fromExample
		}
		f.certain = f.certain || certain
	}

	for _, name := range repo.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if example, real := envFileKind(name); example || real {
			data, ok := readSource(repo, name)
			if !ok {
				continue
			}
			for _, kv := range dotenvLines(data) {
				ev := discovery.Evidence{Path: name, Line: kv.line, Detail: kv.key}
				if example {
					ev.Detail = lineText(data, kv.line)
					add(kv.key, ev, kv.value, true, true)
				} else {
					// A real .env's values are secrets, or may be: only its
					// keys are read, and the evidence names the key alone.
					add(kv.key, ev, "", false, false)
				}
			}
			if real {
				r.Notes = append(r.Notes, discovery.Note{Path: name,
					Message: "This .env file is committed. Loomplane read its keys, not its values. If it holds real secrets, rotate them."})
			}
			continue
		}
		patterns := readsFor(name)
		if patterns == nil || testPath(name) || strings.Contains(path.Base(name), ".min.") {
			continue
		}
		data, ok := readSource(repo, name)
		// Most files read no variable; skip them before running every pattern.
		if !ok || (!bytes.Contains(bytes.ToLower(data), []byte("env")) && !bytes.Contains(data, []byte("${"))) {
			continue
		}
		vite := bytes.Contains(data, []byte("import.meta.env"))
		for i, l := range bytes.Split(data, []byte("\n")) {
			for _, p := range patterns {
				for _, m := range p.re.FindAllSubmatch(l, -1) {
					n := string(m[1])
					if envIgnored[n] || (p.upper && n != strings.ToUpper(n)) {
						continue
					}
					if vite && viteBuiltins[n] && bytes.Contains(l, []byte("import.meta.env."+n)) {
						continue
					}
					def := ""
					if len(m) > 2 {
						def = string(m[2])
					}
					ev := evidence(data, name, i+1)
					if len(ev.Detail) > 200 {
						ev.Detail = ev.Detail[:200] + "…"
					}
					add(n, ev, def, false, true)
				}
			}
		}
	}

	names := make([]string, 0, len(found))
	for n := range found {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f := found[n]
		f.v.Confidence = discovery.Likely
		if f.certain {
			f.v.Confidence = discovery.Certain
		}
		sort.SliceStable(f.v.Evidence, func(i, j int) bool { return f.v.Evidence[i].Path < f.v.Evidence[j].Path })
		r.Env = append(r.Env, *f.v)
	}
	return nil
}

// readSource reads a file a detector scans but doesn't depend on: one it
// can't read, or too big to be hand-written, is passed over without a note.
func readSource(repo discovery.Repo, name string) ([]byte, bool) {
	info, err := fs.Stat(repo.FS, name)
	if err != nil || info.Size() > maxManifestBytes {
		return nil, false
	}
	data, err := fs.ReadFile(repo.FS, name)
	return data, err == nil
}

type dotenvLine struct {
	key, value string
	line       int
}

var dotenvKey = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_.]*)\s*=\s*(.*)$`)

// dotenvLines reads KEY=value lines. A value loses its quotes, and an
// unquoted one its trailing comment.
func dotenvLines(data []byte) []dotenvLine {
	var out []dotenvLine
	for i, l := range strings.Split(string(data), "\n") {
		m := dotenvKey.FindStringSubmatch(l)
		if m == nil || strings.Contains(m[1], ".") {
			continue
		}
		v := strings.TrimSpace(m[2])
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
			if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
				v = v[1 : end+1]
			}
		} else if c := strings.Index(v, " #"); c >= 0 {
			v = strings.TrimSpace(v[:c])
		} else if strings.HasPrefix(v, "#") {
			v = ""
		}
		out = append(out, dotenvLine{key: m[1], value: v, line: i + 1})
	}
	return out
}
