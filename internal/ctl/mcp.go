package ctl

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xraph/forge/cli"

	"github.com/xraph/loomplane/pkg/agentops"
	"github.com/xraph/loomplane/pkg/client"
	"github.com/xraph/loomplane/pkg/manifest"
)

// SchemaURI is where the MCP server serves loomplane.yaml's JSON schema.
const SchemaURI = "loomplane://schema/loomplane.yaml"

// mcpCommand serves Loomplane's tools to an AI agent over MCP, on
// standard input and output, acting with the API key loomctl uses.
func (a *App) mcpCommand() cli.Command {
	return cli.NewCommand("mcp", "Serve Loomplane's tools to an AI agent over MCP, on standard input and output", func(ctx cli.CommandContext) error {
		c, err := a.client()
		if err != nil {
			return err
		}
		s := NewMCPServer(c, a.opts.Version, MCPOptions{AllowDeploy: ctx.Bool("allow-deploy")})
		return s.Run(ctx.Context(), &mcp.StdioTransport{})
	},
		cli.WithUsage("mcp [--allow-deploy]; set LOOMPLANE_URL and LOOMPLANE_API_KEY in the agent's MCP config"),
		cli.WithFlag(cli.NewBoolFlag("allow-deploy", "", "Offer the tools that approve plans and retry deployments, which change your clouds", false)),
	)
}

// MCPOptions decide what an agent may do.
type MCPOptions struct {
	// AllowDeploy offers approve_plan, retry_deployment and
	// create_rollback. Without it an agent can read, analyze, edit the
	// manifest and plan, and a person approves. The key's own access
	// applies either way: a read-only key can't plan, whatever this says.
	AllowDeploy bool
}

// Outputs. The inputs and every tool's name, description and risk are
// in pkg/agentops, which the built-in agent reads too.
type (
	analysesOutput struct {
		Analyses []client.Analysis `json:"analyses"`
	}
	projectsOutput struct {
		Projects []client.Project `json:"projects"`
	}
	manifestOutput struct {
		Revision int    `json:"revision,omitempty"`
		YAML     string `json:"yaml"`
	}
	plansOutput struct {
		Plans []client.Plan `json:"plans"`
	}
	deploymentsOutput struct {
		Deployments []client.Deployment `json:"deployments"`
	}
	statusOutput struct {
		Environments []environmentStatus `json:"environments"`
	}
	environmentStatus struct {
		Environment string           `json:"environment"`
		Bindings    []client.Binding `json:"bindings"`
	}
	variablesOutput struct {
		Variables []client.Variable `json:"variables"`
	}
)

// mcpHandler adds one tool to a server with its typed handler, which
// decodes and validates the arguments and returns structured content.
type mcpHandler struct {
	input reflect.Type
	add   func(s *mcp.Server, t *mcp.Tool)
}

// handle wraps fn as an mcpHandler. The output schema comes from Out.
func handle[In, Out any](fn func(ctx context.Context, in In) (Out, error)) mcpHandler {
	return mcpHandler{input: reflect.TypeFor[In](), add: func(s *mcp.Server, t *mcp.Tool) {
		mcp.AddTool(s, t, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
			out, err := fn(ctx, in)
			return nil, out, err
		})
	}}
}

// annotations gives a definition's MCP hints: read-only and idempotent for
// a read, destructive or not for a change.
func annotations(d agentops.Definition) *mcp.ToolAnnotations {
	if d.ReadOnly {
		return &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	}
	destructive := d.Destructive
	return &mcp.ToolAnnotations{DestructiveHint: &destructive}
}

// offered reports whether loomctl mcp serves d with opts.
func offered(d agentops.Definition, opts MCPOptions) bool {
	return d.Surfaces&agentops.SurfaceMCP != 0 || (opts.AllowDeploy && d.Surfaces&agentops.SurfaceMCPDeploy != 0)
}

// NewMCPServer returns an MCP server whose tools call Loomplane with c.
func NewMCPServer(c *client.Client, version string, opts MCPOptions) *mcp.Server {
	return newMCPServer(version, opts, newMCPHandlers(c))
}

// newMCPServer serves every definition offered over MCP with its handler.
// It panics when one has no handler, or a handler of another input type.
func newMCPServer(version string, opts MCPOptions, handlers map[string]mcpHandler) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "loomplane", Version: version}, &mcp.ServerOptions{
		Instructions: "Loomplane deploys applications built from one or more repositories to clouds, from a loomplane.yaml manifest. " +
			"The usual flow: analyze a project, draft or edit its manifest (save_manifest), create_plan for an environment, " +
			"read the plan's actions, problems and warnings with get_plan, and then a person approves it, or approve_plan if it's offered. " +
			"Problems block a plan; fix them in the manifest and plan again. Never approve a plan you haven't read.",
	})
	for _, def := range agentops.All() {
		if !offered(def, opts) {
			continue
		}
		h, ok := handlers[def.Name]
		if !ok {
			panic(fmt.Sprintf("loomctl mcp: %s has no handler", def.Name))
		}
		if h.input != def.InputType() {
			panic(fmt.Sprintf("loomctl mcp: %s's handler takes %v, its definition %v", def.Name, h.input, def.InputType()))
		}
		schema, err := def.Schema()
		if err != nil {
			panic(fmt.Sprintf("loomctl mcp: %v", err))
		}
		h.add(s, &mcp.Tool{Name: def.Name, Description: def.Description, InputSchema: schema, Annotations: annotations(def)})
	}

	s.AddResource(&mcp.Resource{
		URI: SchemaURI, Name: "loomplane.yaml schema", MIMEType: "application/schema+json",
		Description: "The JSON schema of loomplane.yaml, the manifest save_manifest takes.",
	}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: SchemaURI, MIMEType: "application/schema+json", Text: string(manifest.Schema),
		}}}, nil
	})
	return s
}

// newMCPHandlers returns a handler for every operation loomctl mcp offers,
// by name, each calling Loomplane with c.
func newMCPHandlers(c *client.Client) map[string]mcpHandler {
	find := func(ctx context.Context, ref string) (*client.Project, error) {
		if ref == "" {
			return nil, errors.New("name the project: its slug, name or ID")
		}
		return c.FindProject(ctx, ref)
	}
	return map[string]mcpHandler{
		"whoami": handle(func(ctx context.Context, _ agentops.NoInput) (*client.Session, error) {
			return c.Session(ctx)
		}),
		"list_projects": handle(func(ctx context.Context, _ agentops.NoInput) (projectsOutput, error) {
			list, err := c.Projects(ctx)
			return projectsOutput{Projects: list}, err
		}),
		"analyze": handle(func(ctx context.Context, in agentops.AnalyzeInput) (analysesOutput, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return analysesOutput{}, err
			}
			queued, err := c.Analyze(ctx, p.ID, client.AnalyzeInput{Sources: in.Sources, Ref: in.Ref})
			if err != nil {
				return analysesOutput{}, err
			}
			out := analysesOutput{Analyses: make([]client.Analysis, 0, len(queued))}
			for _, an := range queued {
				done, werr := c.WaitAnalysis(ctx, an.ID, nil)
				if werr != nil {
					return analysesOutput{}, werr
				}
				out.Analyses = append(out.Analyses, *done)
			}
			return out, nil
		}),
		"get_manifest": handle(func(ctx context.Context, in agentops.ProjectInput) (manifestOutput, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return manifestOutput{}, err
			}
			m, err := c.Manifest(ctx, p.ID)
			if err != nil {
				return manifestOutput{}, err
			}
			return manifestOutput{Revision: m.Revision, YAML: m.YAML}, nil
		}),
		"draft_manifest": handle(func(ctx context.Context, in agentops.ProjectInput) (manifestOutput, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return manifestOutput{}, err
			}
			yaml, err := c.DraftManifest(ctx, p.ID)
			return manifestOutput{YAML: yaml}, err
		}),
		"save_manifest": handle(func(ctx context.Context, in agentops.SaveManifestInput) (manifestOutput, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return manifestOutput{}, err
			}
			m, err := c.SaveManifest(ctx, p.ID, in.YAML)
			if err != nil {
				return manifestOutput{}, err
			}
			return manifestOutput{Revision: m.Revision, YAML: m.YAML}, nil
		}),
		"create_plan": handle(func(ctx context.Context, in agentops.CreatePlanInput) (*client.Plan, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return nil, err
			}
			plan, err := c.CreatePlan(ctx, p.ID, client.NewPlan{Environment: in.Environment, Scope: in.Scope, From: in.From})
			if err != nil {
				return nil, err
			}
			return c.WaitPlan(ctx, plan.ID)
		}),
		"list_plans": handle(func(ctx context.Context, in agentops.ProjectInput) (plansOutput, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return plansOutput{}, err
			}
			list, err := c.Plans(ctx, p.ID)
			return plansOutput{Plans: list}, err
		}),
		"get_plan": handle(func(ctx context.Context, in agentops.PlanInput) (*client.Plan, error) {
			return c.Plan(ctx, in.PlanID)
		}),
		"reject_plan": handle(func(ctx context.Context, in agentops.RejectInput) (*client.Plan, error) {
			return c.Reject(ctx, in.PlanID, in.Note)
		}),
		"list_deployments": handle(func(ctx context.Context, in agentops.ProjectInput) (deploymentsOutput, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return deploymentsOutput{}, err
			}
			list, err := c.Deployments(ctx, p.ID)
			return deploymentsOutput{Deployments: list}, err
		}),
		"get_deployment": handle(func(ctx context.Context, in agentops.DeploymentInput) (*client.Deployment, error) {
			return c.Deployment(ctx, in.DeploymentID)
		}),
		"get_release_watch": handle(func(ctx context.Context, in agentops.ReleaseWatchInput) (*client.ReleaseWatch, error) {
			return c.ReleaseWatch(ctx, in.DeploymentID)
		}),
		"deployment_for_plan": handle(func(ctx context.Context, in agentops.PlanInput) (*client.Deployment, error) {
			return c.DeploymentForPlan(ctx, in.PlanID)
		}),
		"status": handle(func(ctx context.Context, in agentops.ProjectInput) (statusOutput, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return statusOutput{}, err
			}
			envs, err := c.Environments(ctx, p.ID)
			if err != nil {
				return statusOutput{}, err
			}
			out := statusOutput{Environments: []environmentStatus{}}
			for _, e := range envs {
				b, berr := c.Bindings(ctx, p.ID, e.Name)
				if berr != nil {
					return statusOutput{}, berr
				}
				out.Environments = append(out.Environments, environmentStatus{Environment: e.Name, Bindings: b})
			}
			return out, nil
		}),
		"list_variables": handle(func(ctx context.Context, in agentops.ProjectInput) (variablesOutput, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return variablesOutput{}, err
			}
			list, err := c.Variables(ctx, p.ID)
			return variablesOutput{Variables: list}, err
		}),
		"set_variable": handle(func(ctx context.Context, in agentops.SetVariableInput) (*client.Variable, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return nil, err
			}
			return c.SetVariable(ctx, p.ID, client.NewVariable{
				Key: in.Key, Value: in.Value, Secret: in.Secret, Environment: in.Environment, Service: in.Service,
			})
		}),

		// Offered with --allow-deploy only; the definitions say so.
		"approve_plan": handle(func(ctx context.Context, in agentops.ApproveInput) (*client.Plan, error) {
			if in.Revision == "" {
				return nil, errors.New("give the revision you reviewed, from get_plan")
			}
			return c.Approve(ctx, in.PlanID, in.Revision, in.Note)
		}),
		"retry_deployment": handle(func(ctx context.Context, in agentops.DeploymentInput) (*client.Deployment, error) {
			return c.Retry(ctx, in.DeploymentID)
		}),
		"create_rollback": handle(func(ctx context.Context, in agentops.CreateRollbackInput) (*client.Plan, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return nil, err
			}
			plan, err := c.CreateRollback(ctx, p.ID, client.NewRollback{Environment: in.Environment, Reason: in.Reason, DeploymentID: in.DeploymentID})
			if err != nil {
				return nil, err
			}
			return c.WaitPlan(ctx, plan.ID)
		}),
	}
}
