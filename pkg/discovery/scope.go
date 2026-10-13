package discovery

import (
	"path"
	"strings"
)

// ScopeExclusion explains why a path is outside the default application.
// Selecting a root explicitly allows its ancestors, while nested example
// and development directories still need their own root or build target.
func ScopeExclusion(name, root string) string {
	name = path.Clean(name)
	root = path.Clean(root)
	if root != "." {
		if name == root {
			return ""
		}
		name = strings.TrimPrefix(name, root+"/")
	}
	for _, part := range strings.Split(name, "/") {
		switch strings.ToLower(part) {
		case "example", "examples", "sample", "samples", "fixture", "fixtures", "testdata", "tests", "__tests__":
			return "example or test directory"
		case "scripts", "tools", "tooling", "generators", "codegen", "generated":
			return "development helper directory"
		}
	}
	return ""
}

// ExplicitTarget reports whether Forge declared an executable as an app
// or build target. Detectors attach the declaration to that entrypoint.
func ExplicitTarget(evidence []Evidence) bool {
	for _, ev := range evidence {
		if base := path.Base(ev.Path); base == ".forge.yaml" || base == ".forge.yml" {
			return true
		}
	}
	return false
}
