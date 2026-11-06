package agentops

import (
	"encoding/json"
	"os/exec"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

func TestDefinitionsAreWellFormed(t *testing.T) {
	defs := All()
	if len(defs) == 0 {
		t.Fatal("no definitions")
	}
	if !sort.SliceIsSorted(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name }) {
		t.Error("All() isn't sorted by name")
	}
	seen := map[string]bool{}
	for _, d := range defs {
		if seen[d.Name] {
			t.Errorf("%s: defined twice", d.Name)
		}
		seen[d.Name] = true
		if !namePattern.MatchString(d.Name) {
			t.Errorf("%q: name doesn't match %s", d.Name, namePattern)
		}
		if d.Description == "" || !strings.HasSuffix(d.Description, ".") {
			t.Errorf("%s: description %q is empty or doesn't end with a full stop", d.Name, d.Description)
		}
		if strings.ContainsRune(d.Description, '\u2014') {
			t.Errorf("%s: description has an em dash", d.Name)
		}
		if !d.Risk.Valid() {
			t.Errorf("%s: risk %q isn't valid", d.Name, d.Risk)
		}
		if d.ReadOnly && d.Risk != RiskRead {
			t.Errorf("%s: read-only but risk %q", d.Name, d.Risk)
		}
		if d.ReadOnly && d.Destructive {
			t.Errorf("%s: read-only and destructive", d.Name)
		}
		if d.External && d.Risk != RiskSecretOrExternal {
			t.Errorf("%s: external but risk %q", d.Name, d.Risk)
		}
		if d.External && d.Surfaces&(SurfaceMCP|SurfaceMCPDeploy) != 0 {
			t.Errorf("%s: external but offered over MCP", d.Name)
		}
		if d.Surfaces == 0 {
			t.Errorf("%s: no surface", d.Name)
		}
		if d.InputType() == nil || d.InputType().Kind() != reflect.Struct {
			t.Errorf("%s: input type %v isn't a struct", d.Name, d.InputType())
		}
		s, err := d.Schema()
		if err != nil {
			t.Errorf("%s: schema: %v", d.Name, err)
			continue
		}
		if s["type"] != "object" || s["additionalProperties"] != false {
			t.Errorf("%s: schema isn't a closed object: %v", d.Name, s)
		}
		props, _ := s["properties"].(map[string]any)
		for field, p := range props {
			desc, _ := p.(map[string]any)["description"].(string)
			if desc == "" {
				t.Errorf("%s: %s has no description", d.Name, field)
			}
			if strings.ContainsRune(desc, '\u2014') {
				t.Errorf("%s: %s's description has an em dash", d.Name, field)
			}
		}
		got, ok := Lookup(d.Name)
		if !ok || got.Name != d.Name {
			t.Errorf("Lookup(%q) = %v, %v", d.Name, got.Name, ok)
		}
	}
	if _, ok := Lookup("no_such_tool"); ok {
		t.Error("Lookup finds an unknown name")
	}
}

func TestBuiltinRiskTable(t *testing.T) {
	agentMCP := SurfaceAgent | SurfaceMCP
	agentDeploy := SurfaceAgent | SurfaceMCPDeploy
	type row struct {
		risk                                      Risk
		readOnly, fromInput, destructive, externl bool
		surfaces                                  Surface
	}
	read := row{risk: RiskRead, readOnly: true, surfaces: agentMCP}
	change := row{risk: RiskChange, surfaces: agentMCP}
	agentRead := row{risk: RiskRead, readOnly: true, surfaces: SurfaceAgent}
	agentChange := row{risk: RiskChange, surfaces: SurfaceAgent}
	ask := row{risk: RiskSecretOrExternal, externl: true, surfaces: SurfaceAgent}
	want := map[string]row{
		"whoami": read, "list_projects": read, "get_manifest": read, "draft_manifest": read,
		"list_plans": read, "get_plan": read, "list_deployments": read, "get_deployment": read,
		"deployment_for_plan": read, "status": read, "list_variables": read, "get_release_watch": read,

		"analyze": change, "save_manifest": change, "create_plan": change, "reject_plan": change,
		"set_variable":    {risk: RiskChange, fromInput: true, surfaces: agentMCP},
		"create_rollback": {risk: RiskChange, destructive: true, surfaces: agentDeploy},

		"approve_plan":     {risk: RiskDeployNonProduction, fromInput: true, destructive: true, surfaces: agentDeploy},
		"retry_deployment": {risk: RiskDeployNonProduction, fromInput: true, destructive: true, surfaces: agentDeploy},

		"github_status": agentRead, "list_repositories": agentRead, "deployed": agentRead,
		"deployment_logs": agentRead, "drift": agentRead, "binding_metrics": agentRead,
		"canary_report": agentRead, "recent_canaries": agentRead, "rollback_target": agentRead,
		"list_teams": agentRead, "get_repository_state": agentRead,

		"create_project": agentChange, "create_environment": agentChange, "add_source": agentChange,

		"ask_person": ask, "request_secret": ask, "request_github_connection": ask, "request_cloud_connection": ask,
	}
	got := map[string]row{}
	for _, d := range All() {
		got[d.Name] = row{d.Risk, d.ReadOnly, d.RiskFromInput, d.Destructive, d.External, d.Surfaces}
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("%s: not defined", name)
			continue
		}
		if g != w {
			t.Errorf("%s: got %+v, want %+v", name, g, w)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("%s: defined but not in the risk table", name)
		}
	}
}

func TestRiskChangesSomething(t *testing.T) {
	for _, r := range []Risk{RiskChange, RiskDeployNonProduction, RiskPromoteProduction, RiskDestroyProduction, RiskSecretOrExternal} {
		if !r.Valid() || !r.ChangesSomething() {
			t.Errorf("%s: valid %v, changes something %v", r, r.Valid(), r.ChangesSomething())
		}
	}
	if !RiskRead.Valid() || RiskRead.ChangesSomething() {
		t.Error("read: should be valid and change nothing")
	}
	if Risk("delete_everything").Valid() || Risk("").Valid() {
		t.Error("an unknown risk is valid")
	}
}

func TestSchemaRejectsUnknownProperties(t *testing.T) {
	d, ok := Lookup("get_manifest")
	if !ok {
		t.Fatal("get_manifest isn't defined")
	}
	s, err := d.Schema()
	if err != nil {
		t.Fatal(err)
	}
	if s["type"] != "object" || s["additionalProperties"] != false {
		t.Errorf("not a closed object: %v", s)
	}
	if req, _ := s["required"].([]any); !slices.Equal(req, []any{"project"}) {
		t.Errorf("required = %v, want [project]", s["required"])
	}
	props, _ := s["properties"].(map[string]any)
	project, _ := props["project"].(map[string]any)
	if project["description"] != "the project's slug, name or ID" {
		t.Errorf("project description = %v", project["description"])
	}
	if _, ok := s["$schema"]; ok {
		t.Error("$schema is set")
	}
}

// The schema matches cortex's tool.Schema: the root's $schema and $id are
// cleared, additionalProperties is the false schema and numbers decode as
// json.Number. Task 12 compares the two directly.
func TestSchemaHasCortexShape(t *testing.T) {
	s := &jsonschema.Schema{
		Schema: "https://json-schema.org/draft/2020-12/schema", ID: "urn:x", Type: "object",
		Properties: map[string]*jsonschema.Schema{"n": {Type: "integer", Minimum: jsonschema.Ptr(1.5)}},
	}
	m, err := schemaMap(closeRoot(s))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["$schema"]; ok {
		t.Error("$schema kept")
	}
	if _, ok := m["$id"]; ok {
		t.Error("$id kept")
	}
	if m["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v", m["additionalProperties"])
	}
	minimum := m["properties"].(map[string]any)["n"].(map[string]any)["minimum"]
	if _, ok := minimum.(json.Number); !ok {
		t.Errorf("minimum is %T, want json.Number", minimum)
	}
}

func TestDefineRefusesNonObjectInputs(t *testing.T) {
	d := Define[string]("bad", "Bad.", RiskRead)
	if _, err := d.Schema(); err == nil {
		t.Error("a string input gave a schema")
	}
	if _, err := (Definition{Name: "zero"}).Schema(); err == nil {
		t.Error("a zero Definition gave a schema")
	}
}

func TestDefineOptions(t *testing.T) {
	d := Define[ProjectInput]("x_y", "X.", RiskChange, Destructive(), FromInput(), On(SurfaceAgent|SurfaceMCP))
	if !d.Destructive || !d.RiskFromInput || d.ReadOnly || d.External || d.Surfaces != SurfaceAgent|SurfaceMCP {
		t.Errorf("options not applied: %+v", d)
	}
	if d.InputType() != reflect.TypeFor[ProjectInput]() {
		t.Errorf("input type %v", d.InputType())
	}
	if Define[NoInput]("a_b", "A.", RiskRead).Surfaces != SurfaceAgent {
		t.Error("the default surface isn't the agent")
	}
}

func TestAskSchemasCarryTeamFields(t *testing.T) {
	for _, name := range []string{"ask_person", "request_secret", "request_github_connection", "request_cloud_connection"} {
		d, _ := Lookup(name)
		s, err := d.Schema()
		if err != nil {
			t.Fatal(err)
		}
		props, _ := s["properties"].(map[string]any)
		team, _ := props["team"].(map[string]any)
		reason, _ := props["team_reason"].(map[string]any)
		if team["description"] != "a team's slug from list_teams, when another team should hear about this" {
			t.Errorf("%s: team = %v", name, team)
		}
		if reason["description"] != "why that team; required with team" {
			t.Errorf("%s: team_reason = %v", name, reason)
		}
		if req, _ := s["required"].([]any); slices.Contains(req, "team") || slices.Contains(req, "team_reason") {
			t.Errorf("%s: team fields are required: %v", name, req)
		}
	}
	approve, _ := Lookup("approve_plan")
	s, err := approve.Schema()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s["properties"].(map[string]any)["team"]; ok {
		t.Error("approve_plan takes a team; it always goes to the project's team")
	}
}

func TestPkgAgentopsHasNoHeavyImports(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range strings.Fields(string(out)) {
		for _, banned := range []string{
			"github.com/xraph/loomplane/internal", "github.com/xraph/cortex", "github.com/xraph/nexus",
			"github.com/xraph/shield", "github.com/xraph/weave",
		} {
			if dep == banned || strings.HasPrefix(dep, banned+"/") {
				t.Errorf("pkg/agentops depends on %s", dep)
			}
		}
	}
}
