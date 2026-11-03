// Package agentops defines Loomplane's operations once: each one's name,
// description, input type and risk class. The built-in agent gives them
// in-process handlers, and loomctl mcp serves the ones it offers over MCP
// with handlers that call the API. Nothing here imports Loomplane's
// internal packages or the AI libraries, so loomctl can link it.
package agentops

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
)

// Risk is an operation's risk class. The agent's tool authorizer decides
// from it whether a call runs, waits for a person or is refused. The
// values match shield's presets.
type Risk string

const (
	RiskRead                Risk = "read"
	RiskChange              Risk = "change"
	RiskDeployNonProduction Risk = "deploy_nonproduction"
	RiskPromoteProduction   Risk = "promote_production"
	RiskDestroyProduction   Risk = "destroy_production"
	RiskSecretOrExternal    Risk = "secret_or_external" //nolint:gosec // G101: a risk class name, not a credential
)

// Valid reports whether r is one of the six classes.
func (r Risk) Valid() bool {
	switch r {
	case RiskRead, RiskChange, RiskDeployNonProduction, RiskPromoteProduction, RiskDestroyProduction, RiskSecretOrExternal:
		return true
	}
	return false
}

// ChangesSomething reports whether r is anything but a read.
func (r Risk) ChangesSomething() bool { return r != RiskRead }

// Surface is where an operation is offered. Definitions combine them.
type Surface uint8

const (
	// SurfaceAgent is the built-in agent.
	SurfaceAgent Surface = 1 << iota
	// SurfaceMCP is loomctl mcp.
	SurfaceMCP
	// SurfaceMCPDeploy is loomctl mcp with --allow-deploy only.
	SurfaceMCPDeploy
)

// Definition is one operation: what it's called, what the model reads
// about it, what it takes and how risky it is.
type Definition struct {
	Name        string
	Description string
	// Risk is the operation's class. When RiskFromInput is set, the
	// handler computes the class from the input and Risk is the lowest
	// it can give.
	Risk          Risk
	RiskFromInput bool
	// ReadOnly means the operation changes nothing, so it is safe to
	// retry after a crash. MCP gets readOnlyHint and idempotentHint.
	ReadOnly bool
	// Destructive gives MCP destructiveHint true; a change without it
	// gives destructiveHint false.
	Destructive bool
	// External marks a request to a person: a cortex external tool,
	// offered to the agent only.
	External bool
	Surfaces Surface

	input  reflect.Type
	schema func() (*jsonschema.Schema, error)
}

// Option changes a Definition as Define builds it.
type Option func(*Definition)

// Define returns the definition of an operation that takes In, which must
// be a struct. It is offered to the agent unless On says otherwise.
func Define[In any](name, description string, risk Risk, opts ...Option) Definition {
	d := Definition{
		Name: name, Description: description, Risk: risk, Surfaces: SurfaceAgent,
		input:  reflect.TypeFor[In](),
		schema: func() (*jsonschema.Schema, error) { return jsonschema.For[In](nil) },
	}
	for _, o := range opts {
		o(&d)
	}
	return d
}

// ReadOnly marks an operation that changes nothing.
func ReadOnly() Option { return func(d *Definition) { d.ReadOnly = true } }

// Destructive marks an operation an MCP client should treat as destructive.
func Destructive() Option { return func(d *Definition) { d.Destructive = true } }

// External marks a request to a person, answered outside the run.
func External() Option { return func(d *Definition) { d.External = true } }

// FromInput says the handler computes the risk class from the input.
func FromInput() Option { return func(d *Definition) { d.RiskFromInput = true } }

// On sets where the operation is offered, replacing the default.
func On(s Surface) Option { return func(d *Definition) { d.Surfaces = s } }

// InputType is the Go type the operation's input decodes into.
func (d Definition) InputType() reflect.Type { return d.input }

// Schema returns the JSON schema of the operation's input as plain JSON
// values. It is what cortex's tool.Schema gives for the same type: the
// root is a closed object with no $schema or $id, and numbers decode as
// json.Number.
func (d Definition) Schema() (map[string]any, error) {
	if d.schema == nil {
		return nil, fmt.Errorf("%s: not built with Define", d.Name)
	}
	s, err := d.schema()
	if err != nil {
		return nil, fmt.Errorf("%s: generate schema: %w", d.Name, err)
	}
	if s.Type != "object" {
		return nil, fmt.Errorf("%s: input must be a struct", d.Name)
	}
	return schemaMap(closeRoot(s))
}

// closeRoot clears the root's $schema and $id and refuses properties the
// input doesn't name.
func closeRoot(s *jsonschema.Schema) *jsonschema.Schema {
	s.Schema = ""
	s.ID = ""
	s.AdditionalProperties = &jsonschema.Schema{Not: &jsonschema.Schema{}}
	return s
}

// schemaMap round-trips s through JSON into a generic map, with numbers
// as json.Number.
func schemaMap(s *jsonschema.Schema) (map[string]any, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("encode schema: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}
	return out, nil
}
