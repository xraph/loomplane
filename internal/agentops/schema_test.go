package agentops

import (
	"reflect"
	"testing"

	"github.com/xraph/cortex/tool"
	"github.com/xraph/shield/presets"

	defs "github.com/xraph/loomplane/pkg/agentops"
)

// schemaOf is cortex's tool.Schema for each input type pkg/agentops
// defines. A type missing here fails TestCortexAndPkgSchemasAgree.
var schemaOf = map[reflect.Type]func() (map[string]any, error){
	reflect.TypeFor[defs.NoInput]():                tool.Schema[defs.NoInput],
	reflect.TypeFor[defs.ProjectInput]():           tool.Schema[defs.ProjectInput],
	reflect.TypeFor[defs.PlanInput]():              tool.Schema[defs.PlanInput],
	reflect.TypeFor[defs.DeploymentInput]():        tool.Schema[defs.DeploymentInput],
	reflect.TypeFor[defs.AnalyzeInput]():           tool.Schema[defs.AnalyzeInput],
	reflect.TypeFor[defs.SaveManifestInput]():      tool.Schema[defs.SaveManifestInput],
	reflect.TypeFor[defs.CreatePlanInput]():        tool.Schema[defs.CreatePlanInput],
	reflect.TypeFor[defs.ApproveInput]():           tool.Schema[defs.ApproveInput],
	reflect.TypeFor[defs.RejectInput]():            tool.Schema[defs.RejectInput],
	reflect.TypeFor[defs.SetVariableInput]():       tool.Schema[defs.SetVariableInput],
	reflect.TypeFor[defs.CreateRollbackInput]():    tool.Schema[defs.CreateRollbackInput],
	reflect.TypeFor[defs.ReleaseWatchInput]():      tool.Schema[defs.ReleaseWatchInput],
	reflect.TypeFor[defs.CreateProjectInput]():     tool.Schema[defs.CreateProjectInput],
	reflect.TypeFor[defs.CreateEnvironmentInput](): tool.Schema[defs.CreateEnvironmentInput],
	reflect.TypeFor[defs.AddSourceInput]():         tool.Schema[defs.AddSourceInput],
	reflect.TypeFor[defs.EnvironmentInput]():       tool.Schema[defs.EnvironmentInput],
	reflect.TypeFor[defs.DriftInput]():             tool.Schema[defs.DriftInput],
	reflect.TypeFor[defs.DeploymentLogsInput]():    tool.Schema[defs.DeploymentLogsInput],
	reflect.TypeFor[defs.BindingMetricsInput]():    tool.Schema[defs.BindingMetricsInput],
	reflect.TypeFor[defs.RecentCanariesInput]():    tool.Schema[defs.RecentCanariesInput],
	reflect.TypeFor[defs.RollbackTargetInput]():    tool.Schema[defs.RollbackTargetInput],
	reflect.TypeFor[defs.AskPersonInput]():         tool.Schema[defs.AskPersonInput],
	reflect.TypeFor[defs.RequestSecretInput]():     tool.Schema[defs.RequestSecretInput],
	reflect.TypeFor[defs.RequestGitHubInput]():     tool.Schema[defs.RequestGitHubInput],
	reflect.TypeFor[defs.RequestConnectionInput](): tool.Schema[defs.RequestConnectionInput],
}

// pkg/agentops can't import cortex, so it builds its schemas itself. They
// must be exactly what cortex builds for the same type, or the model reads
// one schema and cortex validates the call against another. That holds for
// every definition, and for the tool each registered operation hands
// cortex.
func TestCortexAndPkgSchemasAgree(t *testing.T) {
	for _, d := range defs.All() {
		build, ok := schemaOf[d.InputType()]
		if !ok {
			t.Errorf("%s: add %v to schemaOf", d.Name, d.InputType())
			continue
		}
		want, err := build()
		if err != nil {
			t.Fatalf("%s: cortex: %v", d.Name, err)
		}
		got, err := d.Schema()
		if err != nil {
			t.Fatalf("%s: pkg/agentops: %v", d.Name, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: pkg/agentops schema\n%#v\ncortex's\n%#v", d.Name, got, want)
		}
	}
	w := newWorld(t)
	for _, op := range w.reg.Ops() {
		want, err := op.Def.Schema()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(op.Tool.Parameters, want) {
			t.Errorf("%s: the tool's parameters\n%#v\nthe definition's schema\n%#v", op.Def.Name, op.Tool.Parameters, want)
		}
		if op.Tool.Name != op.Def.Name || op.Tool.Description != op.Def.Description {
			t.Errorf("%s: tool %q described %q", op.Def.Name, op.Tool.Name, op.Tool.Description)
		}
	}
}

// The risk classes are shield's: its tools profile decides by them.
func TestRiskValuesMatchShield(t *testing.T) {
	for got, want := range map[defs.Risk]string{
		defs.RiskRead:                presets.RiskRead,
		defs.RiskChange:              presets.RiskChange,
		defs.RiskDeployNonProduction: presets.RiskDeployNonProduction,
		defs.RiskPromoteProduction:   presets.RiskPromoteProduction,
		defs.RiskDestroyProduction:   presets.RiskDestroyProduction,
		defs.RiskSecretOrExternal:    presets.RiskSecretOrExternal,
	} {
		if string(got) != want {
			t.Errorf("risk %q, shield's %q", got, want)
		}
	}
}
