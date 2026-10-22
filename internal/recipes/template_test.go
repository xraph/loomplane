package recipes

import (
	"reflect"
	"testing"
)

func TestParseTemplate(t *testing.T) {
	parts, problems := ParseTemplate("postgres://u:${secret:pw}@${dep:postgres:hostport}/db?${component:api:url:http}")
	want := []TemplatePart{
		{Text: "postgres://u:"},
		{Text: "secret:pw", Placeholder: true, Secret: "pw"},
		{Text: "@"},
		{Text: "dep:postgres:hostport", Placeholder: true, Ref: &Ref{Dependency: "postgres", Field: "hostport"}},
		{Text: "/db?"},
		{Text: "component:api:url:http", Placeholder: true, Ref: &Ref{Component: "api", Field: "url:http"}},
	}
	if len(problems) != 0 || !reflect.DeepEqual(parts, want) {
		t.Fatalf("parts %+v, problems %v", parts, problems)
	}
	if parts, problems = ParseTemplate("plain"); len(problems) != 0 || !reflect.DeepEqual(parts, []TemplatePart{{Text: "plain"}}) {
		t.Fatalf("plain: %+v %v", parts, problems)
	}
	// A bad placeholder is left out and reported; an unterminated one ends
	// the parse.
	parts, problems = ParseTemplate("a${env:HOME}b${secret:x}c${dep:y")
	if want := []string{
		`placeholder "${env:HOME}" is not ${dep:<dependency>:<field>}, ${secret:<name>} or ${component:<name>:<field>}`,
		`has an unterminated placeholder "${dep:y"`,
	}; !reflect.DeepEqual(problems, want) {
		t.Fatalf("problems %q", problems)
	}
	if want := []TemplatePart{{Text: "a"}, {Text: "b"}, {Text: "secret:x", Placeholder: true, Secret: "x"}, {Text: "c"}}; !reflect.DeepEqual(parts, want) {
		t.Fatalf("parts %+v", parts)
	}
}
