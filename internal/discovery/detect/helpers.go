// Package detect holds Loomplane's detectors: Go, Rust, Node, Python, the
// JVM, Dockerfiles and Compose files. Each reads manifests and source headers from a
// checkout and reports what it finds with the line it found it on. None of
// them runs anything.
package detect

import (
	"bytes"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/xraph/loomplane/pkg/discovery"
)

// maxManifestBytes caps how much of one manifest a detector reads. Real
// manifests are a few kilobytes; anything bigger is skipped with a note.
const maxManifestBytes = 1 << 20

// All returns every detector, in the order they run.
func All() []discovery.Detector {
	return []discovery.Detector{Go{}, Forge{}, Rust{}, Node{}, Python{}, JVM{}, Docker{}, Compose{}}
}

// read returns a file's contents, or a note saying why it couldn't.
func read(repo discovery.Repo, name string) ([]byte, *discovery.Note) {
	info, err := fs.Stat(repo.FS, name)
	if err != nil {
		return nil, &discovery.Note{Path: name, Message: "unreadable: " + err.Error()}
	}
	if info.Size() > maxManifestBytes {
		return nil, &discovery.Note{Path: name, Message: "skipped: larger than 1 MiB"}
	}
	data, err := fs.ReadFile(repo.FS, name)
	if err != nil {
		return nil, &discovery.Note{Path: name, Message: "unreadable: " + err.Error()}
	}
	return data, nil
}

func note(r *discovery.Report, n *discovery.Note) {
	if n != nil {
		r.Notes = append(r.Notes, *n)
	}
}

// dir is the directory of a slash-separated file path, "." at the root.
func dir(name string) string { return path.Dir(name) }

// join joins slash-separated paths and cleans the result, "." at the root.
func join(elem ...string) string { return path.Clean(path.Join(elem...)) }

// within reports whether name is dir itself or inside it.
func within(name, dir string) bool {
	if dir == "." {
		return true
	}
	return name == dir || strings.HasPrefix(name, dir+"/")
}

// lineOf returns the 1-based line of the first match of re at or after
// line from (1-based), or 0.
func lineOf(data []byte, re *regexp.Regexp, from int) int {
	for i, l := range bytes.Split(data, []byte("\n")) {
		if i+1 < from {
			continue
		}
		if re.Match(l) {
			return i + 1
		}
	}
	return 0
}

// lineText returns the trimmed text of a 1-based line.
func lineText(data []byte, line int) string {
	if line < 1 {
		return ""
	}
	lines := bytes.Split(data, []byte("\n"))
	if line > len(lines) {
		return ""
	}
	return strings.TrimSpace(string(lines[line-1]))
}

// evidence points at line in name, quoting it.
func evidence(data []byte, name string, line int) discovery.Evidence {
	return discovery.Evidence{Path: name, Line: line, Detail: lineText(data, line)}
}

// matchGlob reports whether a slash-separated path matches a workspace
// glob: "*" matches within one segment, "**" any number of segments.
func matchGlob(pattern, name string) bool {
	pattern = strings.TrimPrefix(path.Clean(strings.TrimPrefix(pattern, "./")), "./")
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if matchSegments(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], name[0]); err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// expand returns the candidate directories the patterns select. A pattern
// starting with "!" removes what it matches.
func expand(patterns, candidates []string) []string {
	var out []string
	for _, c := range candidates {
		in := false
		for _, p := range patterns {
			if neg, ok := strings.CutPrefix(p, "!"); ok {
				if matchGlob(neg, c) {
					in = false
				}
				continue
			}
			if matchGlob(p, c) {
				in = true
			}
		}
		if in {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// owner returns the deepest of dirs containing name, or "".
func owner(name string, dirs []string) string {
	best, bestDepth := "", -1
	for _, d := range dirs {
		if !within(name, d) {
			continue
		}
		depth := 0
		if d != "." {
			depth = strings.Count(d, "/") + 1
		}
		if depth > bestDepth {
			best, bestDepth = d, depth
		}
	}
	return best
}

// addResource adds a resource use unless one of the same kind and file is
// already there.
func addResource(list []discovery.ResourceUse, r discovery.ResourceUse) []discovery.ResourceUse {
	for i, x := range list {
		if x.Kind == r.Kind && x.Evidence.Path == r.Evidence.Path {
			if r.Confidence.Rank() > x.Confidence.Rank() {
				list[i] = r
			}
			return list
		}
	}
	return append(list, r)
}

// addFramework adds a framework unless it's already there.
func addFramework(list []discovery.Framework, f discovery.Framework) []discovery.Framework {
	for _, x := range list {
		if x.Name == f.Name {
			return list
		}
	}
	return append(list, f)
}

func sortResources(list []discovery.ResourceUse) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Kind != list[j].Kind {
			return list[i].Kind < list[j].Kind
		}
		return list[i].Evidence.Path < list[j].Evidence.Path
	})
}

func sortFrameworks(list []discovery.Framework) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })
}
