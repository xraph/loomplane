package federation

import (
	"regexp"
	"strings"
)

var exactHTTPPath = regexp.MustCompile(`^/[A-Za-z0-9/._~+-]*$`)
var exactGRPCMethod = regexp.MustCompile(`^/[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*/[A-Za-z_][A-Za-z0-9_]*$`)

// ValidHTTPMethod accepts the supported, case-sensitive HTTP methods.
func ValidHTTPMethod(method string) bool {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
		return true
	default:
		return false
	}
}

// ValidHTTPPath accepts exact paths without decoding or normalization aliases.
func ValidHTTPPath(path string) bool {
	if len(path) > 1024 || !exactHTTPPath.MatchString(path) || strings.Contains(path, "//") {
		return false
	}
	for segment := range strings.SplitSeq(path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// ValidGRPCMethod accepts an exact fully qualified service and method name.
func ValidGRPCMethod(method string) bool {
	return len(method) <= 256 && exactGRPCMethod.MatchString(method)
}
