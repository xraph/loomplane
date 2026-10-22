package recipes

import (
	"fmt"
	"strings"
)

// TemplatePart is one piece of an EnvVar or Output template: a run of
// literal text, or one ${...} placeholder.
type TemplatePart struct {
	// Text is the literal text, or for a placeholder what's between "${"
	// and "}".
	Text string
	// Placeholder is set for a ${...} placeholder.
	Placeholder bool
	// Ref is what a ${dep:<dependency>:<field>} or
	// ${component:<name>:<field>} placeholder reads.
	Ref *Ref
	// Secret is the generated secret a ${secret:<name>} placeholder reads.
	Secret string
}

// ParseTemplate splits a template into literal text and placeholders, in
// order. Text outside a placeholder is literal. problems describes each
// placeholder that isn't one of the three forms, which parts leaves out,
// and an unterminated one, which ends the parse.
func ParseTemplate(template string) (parts []TemplatePart, problems []string) {
	rest := template
	for {
		start := strings.Index(rest, "${")
		if start < 0 {
			if rest != "" {
				parts = append(parts, TemplatePart{Text: rest})
			}
			return parts, problems
		}
		if start > 0 {
			parts = append(parts, TemplatePart{Text: rest[:start]})
		}
		end := strings.IndexByte(rest[start:], '}')
		if end < 0 {
			return parts, append(problems, fmt.Sprintf("has an unterminated placeholder %q", rest[start:]))
		}
		inner := rest[start+2 : start+end]
		rest = rest[start+end+1:]
		form := strings.SplitN(inner, ":", 3)
		switch {
		case len(form) == 3 && form[0] == "dep":
			parts = append(parts, TemplatePart{Text: inner, Placeholder: true, Ref: &Ref{Dependency: form[1], Field: form[2]}})
		case len(form) == 3 && form[0] == "component":
			parts = append(parts, TemplatePart{Text: inner, Placeholder: true, Ref: &Ref{Component: form[1], Field: form[2]}})
		case len(form) == 2 && form[0] == "secret":
			parts = append(parts, TemplatePart{Text: inner, Placeholder: true, Secret: form[1]})
		default:
			problems = append(problems, fmt.Sprintf("placeholder %q is not ${dep:<dependency>:<field>}, ${secret:<name>} or ${component:<name>:<field>}", "${"+inner+"}"))
		}
	}
}
