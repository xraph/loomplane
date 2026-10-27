package engines

import (
	"bufio"
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/models/registry"
)

// Each list file is named <engine>-<version>-<list>.txt, starts with a "#"
// line naming the image it came from, and holds one entry per line. Blank
// lines and "#" lines are skipped.
//
//go:embed testdata/*.txt
var testdataFS embed.FS

// The lists a capture can hold.
const (
	listFlags            = "flags"
	listArchitectures    = "architectures"
	listToolParsers      = "tool-parsers"
	listReasoningParsers = "reasoning-parsers"
)

// wantLists names the lists each engine must have. TEI's flags are the
// fixed list below rather than a captured file. llama.cpp has no
// architecture list because it reads the architecture from the GGUF file.
var wantLists = map[registry.Engine][]string{
	registry.EngineVLLM:     {listFlags, listArchitectures, listToolParsers, listReasoningParsers},
	registry.EngineTEI:      {listArchitectures},
	registry.EngineLlamaCPP: {listFlags},
}

// teiFlags is the part of TEI 1.9.4's --help Loomplane uses.
var teiFlags = []string{
	"--model-id", "--revision", "--pooling", "--max-concurrent-requests",
	"--max-batch-tokens", "--max-client-batch-size", "--auto-truncate",
	"--hostname", "--port", "--huggingface-hub-cache", "--served-model-name",
	"--dtype", "--default-prompt-name", "--dense-path",
}

// capabilities holds every list, keyed by engine and then list name.
type capabilities map[registry.Engine]map[string][]string

var known = mustLoadCaptured()

func mustLoadCaptured() capabilities {
	sub, err := fs.Sub(testdataFS, "testdata")
	if err != nil {
		panic(fmt.Sprintf("engines: embedded lists: %v", err))
	}
	c, err := loadCaptured(sub, Current())
	if err != nil {
		panic(fmt.Sprintf("engines: embedded lists: %v", err))
	}
	return c
}

// loadCaptured reads every *.txt list at the top of fsys. Each one must be
// for the version in current, so a list left over from an older release
// can't vouch for the new one.
func loadCaptured(fsys fs.FS, current map[registry.Engine]string) (capabilities, error) {
	names, err := fs.Glob(fsys, "*.txt")
	if err != nil {
		return nil, err
	}
	c := capabilities{}
	for _, name := range names {
		engine, version, list, err := parseListName(name)
		if err != nil {
			return nil, err
		}
		if want := current[engine]; version != want {
			return nil, fmt.Errorf("%s is for %s %s, but Loomplane pins %s: rerun scripts/capture-model-engines.sh", name, engine, version, want)
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		entries, err := parseList(name, data)
		if err != nil {
			return nil, err
		}
		if c[engine] == nil {
			c[engine] = map[string][]string{}
		}
		c[engine][list] = entries
	}
	for engine, lists := range wantLists {
		for _, list := range lists {
			if _, ok := c[engine][list]; !ok {
				return nil, fmt.Errorf("missing %s-%s-%s.txt", engine, current[engine], list)
			}
		}
	}
	return c, nil
}

// parseListName splits "vllm-0.31.0-tool-parsers.txt" into its engine,
// version and list.
func parseListName(name string) (engine registry.Engine, version, list string, err error) {
	base := strings.TrimSuffix(path.Base(name), ".txt")
	prefix, rest, ok := strings.Cut(base, "-")
	engine = registry.Engine(prefix)
	lists, isEngine := wantLists[engine]
	if !ok || !isEngine {
		return "", "", "", fmt.Errorf("%s: want <engine>-<version>-<list>.txt with engine vllm, tei or llamacpp", name)
	}
	for _, l := range lists {
		if v, found := strings.CutSuffix(rest, "-"+l); found && v != "" {
			return engine, v, l, nil
		}
	}
	return "", "", "", fmt.Errorf("%s: %s lists are %s", name, engine, strings.Join(lists, ", "))
}

// parseList reads one entry per line, after the header that names the
// image.
func parseList(name string, data []byte) ([]string, error) {
	if !bytes.HasPrefix(data, []byte("#")) {
		return nil, fmt.Errorf("%s: the first line must be a # line naming the image it came from", name)
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if len(out) == 0 {
		return nil, errors.New(name + ": no entries")
	}
	if strings.HasSuffix(strings.TrimSuffix(name, ".txt"), "-"+listFlags) {
		for _, f := range out {
			if !strings.HasPrefix(f, "--") {
				return nil, fmt.Errorf("%s: %q isn't a long flag", name, f)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// KnownFlag reports whether the pinned release of e accepts flag, written
// without a value, as in "--max-model-len".
func KnownFlag(e registry.Engine, flag string) bool {
	if e == registry.EngineTEI {
		return slices.Contains(teiFlags, flag)
	}
	return has(known[e][listFlags], flag)
}

// ToolParsers returns the --tool-call-parser values the pinned vLLM accepts.
func ToolParsers() []string {
	return slices.Clone(known[registry.EngineVLLM][listToolParsers])
}

// ReasoningParsers returns the --reasoning-parser values the pinned vLLM
// accepts.
func ReasoningParsers() []string {
	return slices.Clone(known[registry.EngineVLLM][listReasoningParsers])
}

// SupportsArchitecture reports whether the pinned release of e serves a
// model whose config.json names arch first. llama.cpp always reports false:
// it has no such list, because it reads GGUF files.
func SupportsArchitecture(e registry.Engine, arch string) bool {
	return has(known[e][listArchitectures], arch)
}

// has searches a sorted list.
func has(list []string, s string) bool {
	_, ok := slices.BinarySearch(list, s)
	return ok
}
