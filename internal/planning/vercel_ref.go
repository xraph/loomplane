package planning

import "strings"

// VercelRef is a Vercel service binding's ExternalID when it deploys into
// one environment of a shared Vercel project: the project id and the
// target ("production", "preview" or a custom environment id), joined by
// "#".
func VercelRef(projectID, target string) string {
	return projectID + "#" + target
}

// ParseVercelRef splits a Vercel service binding's ExternalID. A ref with
// no "#" is a project of its own (the fallback, or a binding made before
// native environments), so native is false and target is empty.
func ParseVercelRef(ref string) (projectID, target string, native bool) {
	projectID, target, native = strings.Cut(ref, "#")
	return projectID, target, native
}
