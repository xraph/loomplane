package realtime

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

var terminalCommands = regexp.MustCompile(`(?:\x1b\]|\x{009d})[^\x07\x1b]*(?:\x07|\x1b\\)|(?:\x1b\[|\x{009b})[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]|[\x00-\x08\x0b-\x1f\x7f\x{0080}-\x{009f}]`)

// Redactor contains literal secrets only in worker memory.
type Redactor struct{ values []string }

func NewRedactor(values []string) *Redactor {
	seen := map[string]bool{}
	for _, value := range values {
		if value != "" {
			seen[value] = true
		}
		for _, line := range strings.Split(value, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				seen[line] = true
			}
		}
	}
	out := &Redactor{}
	for value := range seen {
		out.values = append(out.values, value)
	}
	slices.SortFunc(out.values, func(a, b string) int { return len(b) - len(a) })
	return out
}

// Line redacts before bounding the line, then removes terminal commands.
func (r *Redactor) Line(input string, limit int) (string, bool) {
	if r != nil {
		for _, value := range r.values {
			input = strings.ReplaceAll(input, value, "[redacted]")
		}
	}
	input = terminalCommands.ReplaceAllString(strings.ToValidUTF8(input, "�"), "")
	if len(input) <= limit {
		return input, false
	}
	input = input[:limit]
	for !utf8.ValidString(input) {
		input = input[:len(input)-1]
	}
	return input, true
}
