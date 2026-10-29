package ctl

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/xraph/forge/cli"

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

// Inputs and outputs. The descriptions are what the agent reads.
type (
	noInput      struct{}
	projectInput struct {
		Project string `json:"project" jsonschema:"the project's slug, name or ID"`
	}
	planInput struct {
		PlanID string `json:"plan_id" jsonschema:"the plan's ID"`
	}
	deploymentInput struct {
		DeploymentID string `json:"deployment_id" jsonschema:"the deployment's ID"`
	}
	analyzeInput struct {
		Project string   `json:"project" jsonschema:"the project's slug, name or ID"`
		Sources []string `json:"sources,omitempty" jsonschema:"the repositories to analyze, by alias; every one of the project's if empty"`
		Ref     string   `json:"ref,omitempty" jsonschema:"a branch, tag or commit, for one repository only; its tracked branch if empty"`
	}
	saveManifestInput struct {
		Project string `json:"project" jsonschema:"the project's slug, name or ID"`
		YAML    string `json:"yaml" jsonschema:"the whole loomplane.yaml; the schema is the resource loomplane://schema/loomplane.yaml"`
	}
	createPlanInput struct {
		Project     string   `json:"project" jsonschema:"the project's slug, name or ID"`
		Environment string   `json:"environment" jsonschema:"the environment to plan for, such as production"`
		Scope       []string `json:"scope,omitempty" jsonschema:"the repositories the plan moves, by alias; every one if empty, and the rest stay as deployed"`
		From        string   `json:"from,omitempty" jsonschema:"An environment to promote from, such as staging: the plan pins the commits it runs"`
	}
	createRollbackInput struct {
		Project      string `json:"project" jsonschema:"the project's slug, name or ID"`
		Environment  string `json:"environment" jsonschema:"the environment to roll back, such as production"`
		Reason       string `json:"reason" jsonschema:"why, for the audit trail and whoever approves it"`
		DeploymentID string `json:"deployment_id,omitempty" jsonschema:"the deployment to undo; it must be the environment's newest, which is the default"`
	}
	approveInput struct {
		PlanID   string `json:"plan_id" jsonschema:"the plan's ID"`
		Revision string `json:"revision" jsonschema:"the revision of the plan you reviewed, from get_plan; a plan that changed since comes back stale"`
		Note     string `json:"note,omitempty" jsonschema:"why, for the audit trail"`
	}
	rejectInput struct {
		PlanID string `json:"plan_id" jsonschema:"the plan's ID"`
		Note   string `json:"note,omitempty" jsonschema:"why, for the audit trail"`
	}
	setVariableInput struct {
		Project     string `json:"project" jsonschema:"the project's slug, name or ID"`
		Key         string `json:"key" jsonschema:"the variable's name, such as DATABASE_URL"`
		Value       string `json:"value" jsonschema:"its value"`
		Environment string `json:"environment,omitempty" jsonschema:"one environment; every environment if empty"`
		Service     string `json:"service,omitempty" jsonschema:"one service's graph ID; every service if empty"`
		Secret      bool   `json:"secret,omitempty" jsonschema:"keep it secret: write-only, and in the cloud's secret store"`
	}

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

func readOnly() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
}

func destructive() *mcp.ToolAnnotations {
	t := true
	return &mcp.ToolAnnotations{DestructiveHint: &t}
}

func additive() *mcp.ToolAnnotations {
	f := false
	return &mcp.ToolAnnotations{DestructiveHint: &f}
}

// NewMCPServer returns an MCP server whose tools call Loomplane with c.
func NewMCPServer(c *client.Client, version string, opts MCPOptions) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "loomplane", Version: version}, &mcp.ServerOptions{
		Instructions: "Loomplane deploys applications built from one or more repositories to clouds, from a loomplane.yaml manifest. " +
			"The usual flow: analyze a project, draft or edit its manifest (save_manifest), create_plan for an environment, " +
			"read the plan's actions, problems and warnings with get_plan, and then a person approves it, or approve_plan if it's offered. " +
			"Problems block a plan; fix them in the manifest and plan again. Never approve a plan you haven't read.",
	})
	find := func(ctx context.Context, ref string) (*client.Project, error) {
		if ref == "" {
			return nil, errors.New("name the project: its slug, name or ID")
		}
		return c.FindProject(ctx, ref)
	}

	mcp.AddTool(s, &mcp.Tool{Name: "whoami", Description: "Who the API key acts as, in which organization, and the permissions it has.", Annotations: readOnly()},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, *client.Session, error) {
			who, err := c.Session(ctx)
			return nil, who, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "list_projects", Description: "The organization's projects.", Annotations: readOnly()},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, projectsOutput, error) {
			list, err := c.Projects(ctx)
			return nil, projectsOutput{Projects: list}, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "analyze", Description: "Read the project's repositories, or the ones named, and find their services, packages and resources. One analysis per repository; waits until each is done.", Annotations: additive()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in analyzeInput) (*mcp.CallToolResult, analysesOutput, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return nil, analysesOutput{}, err
			}
			queued, err := c.Analyze(ctx, p.ID, client.AnalyzeInput{Sources: in.Sources, Ref: in.Ref})
			if err != nil {
				return nil, analysesOutput{}, err
			}
			out := analysesOutput{Analyses: make([]client.Analysis, 0, len(queued))}
			for _, an := range queued {
				done, werr := c.WaitAnalysis(ctx, an.ID, nil)
				if werr != nil {
					return nil, analysesOutput{}, werr
				}
				out.Analyses = append(out.Analyses, *done)
			}
			return nil, out, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_manifest", Description: "The project's current loomplane.yaml.", Annotations: readOnly()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in projectInput) (*mcp.CallToolResult, manifestOutput, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return nil, manifestOutput{}, err
			}
			m, err := c.Manifest(ctx, p.ID)
			if err != nil {
				return nil, manifestOutput{}, err
			}
			return nil, manifestOutput{Revision: m.Revision, YAML: m.YAML}, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "draft_manifest", Description: "A drafted loomplane.yaml from the project's latest analysis. It isn't saved.", Annotations: readOnly()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in projectInput) (*mcp.CallToolResult, manifestOutput, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return nil, manifestOutput{}, err
			}
			yaml, err := c.DraftManifest(ctx, p.ID)
			return nil, manifestOutput{YAML: yaml}, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "save_manifest", Description: "Save a new revision of the project's loomplane.yaml. A manifest that doesn't parse is refused with every problem and its line.", Annotations: additive()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in saveManifestInput) (*mcp.CallToolResult, manifestOutput, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return nil, manifestOutput{}, err
			}
			m, err := c.SaveManifest(ctx, p.ID, in.YAML)
			if err != nil {
				return nil, manifestOutput{}, err
			}
			return nil, manifestOutput{Revision: m.Revision, YAML: m.YAML}, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "create_plan", Description: "Plan the project's current manifest for an environment: what deploying it would do. With from, it promotes what that environment runs instead. Waits until it's planned. Nothing changes until someone approves it.", Annotations: additive()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in createPlanInput) (*mcp.CallToolResult, *client.Plan, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return nil, nil, err
			}
			plan, err := c.CreatePlan(ctx, p.ID, client.NewPlan{Environment: in.Environment, Scope: in.Scope, From: in.From})
			if err != nil {
				return nil, nil, err
			}
			plan, err = c.WaitPlan(ctx, plan.ID)
			return nil, plan, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "list_plans", Description: "The project's plans, newest first.", Annotations: readOnly()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in projectInput) (*mcp.CallToolResult, plansOutput, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return nil, plansOutput{}, err
			}
			list, err := c.Plans(ctx, p.ID)
			return nil, plansOutput{Plans: list}, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_plan", Description: "A plan: its actions step by step, the problems that block it, its warnings, and its revision.", Annotations: readOnly()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in planInput) (*mcp.CallToolResult, *client.Plan, error) {
			plan, err := c.Plan(ctx, in.PlanID)
			return nil, plan, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "reject_plan", Description: "Reject a plan. Nothing it says happens.", Annotations: additive()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in rejectInput) (*mcp.CallToolResult, *client.Plan, error) {
			plan, err := c.Reject(ctx, in.PlanID, in.Note)
			return nil, plan, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "list_deployments", Description: "The project's deployments, newest first.", Annotations: readOnly()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in projectInput) (*mcp.CallToolResult, deploymentsOutput, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return nil, deploymentsOutput{}, err
			}
			list, err := c.Deployments(ctx, p.ID)
			return nil, deploymentsOutput{Deployments: list}, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_deployment", Description: "A deployment, step by step, with each step's status and message.", Annotations: readOnly()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in deploymentInput) (*mcp.CallToolResult, *client.Deployment, error) {
			d, err := c.Deployment(ctx, in.DeploymentID)
			return nil, d, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "get_release_watch", Description: "How a deployment is watched after it finishes: its status and verdict, the verdict on each changed service, the services it can't watch, and the rollback plan if it breached. A deployment with no watch answers not found. Unverified means missing evidence, so ask a person.", Annotations: readOnly()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in deploymentInput) (*mcp.CallToolResult, *client.ReleaseWatch, error) {
			w, err := c.ReleaseWatch(ctx, in.DeploymentID)
			return nil, w, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "deployment_for_plan", Description: "The deployment an approved plan started.", Annotations: readOnly()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in planInput) (*mcp.CallToolResult, *client.Deployment, error) {
			d, err := c.DeploymentForPlan(ctx, in.PlanID)
			return nil, d, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "status", Description: "What runs each of the project's services and resources in each environment, with its address and any drift.", Annotations: readOnly()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in projectInput) (*mcp.CallToolResult, statusOutput, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return nil, statusOutput{}, err
			}
			envs, err := c.Environments(ctx, p.ID)
			if err != nil {
				return nil, statusOutput{}, err
			}
			out := statusOutput{Environments: []environmentStatus{}}
			for _, e := range envs {
				b, berr := c.Bindings(ctx, p.ID, e.Name)
				if berr != nil {
					return nil, statusOutput{}, berr
				}
				out.Environments = append(out.Environments, environmentStatus{Environment: e.Name, Bindings: b})
			}
			return nil, out, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "list_variables", Description: "The project's variables. A secret's value is never shown.", Annotations: readOnly()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in projectInput) (*mcp.CallToolResult, variablesOutput, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return nil, variablesOutput{}, err
			}
			list, err := c.Variables(ctx, p.ID)
			return nil, variablesOutput{Variables: list}, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "set_variable", Description: "Set a project variable. Services get it on their next deployment.", Annotations: additive()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in setVariableInput) (*mcp.CallToolResult, *client.Variable, error) {
			p, err := find(ctx, in.Project)
			if err != nil {
				return nil, nil, err
			}
			v, err := c.SetVariable(ctx, p.ID, client.NewVariable{
				Key: in.Key, Value: in.Value, Secret: in.Secret, Environment: in.Environment, Service: in.Service,
			})
			return nil, v, err
		})

	if opts.AllowDeploy {
		mcp.AddTool(s, &mcp.Tool{Name: "approve_plan", Description: "Approve a plan at the revision you reviewed, which deploys it to the cloud. Read it with get_plan first.", Annotations: destructive()},
			func(ctx context.Context, _ *mcp.CallToolRequest, in approveInput) (*mcp.CallToolResult, *client.Plan, error) {
				if in.Revision == "" {
					return nil, nil, errors.New("give the revision you reviewed, from get_plan")
				}
				plan, err := c.Approve(ctx, in.PlanID, in.Revision, in.Note)
				return nil, plan, err
			})
		mcp.AddTool(s, &mcp.Tool{Name: "retry_deployment", Description: "Retry a failed deployment from the step that failed, with the plan that was approved.", Annotations: destructive()},
			func(ctx context.Context, _ *mcp.CallToolRequest, in deploymentInput) (*mcp.CallToolResult, *client.Deployment, error) {
				d, err := c.Retry(ctx, in.DeploymentID)
				return nil, d, err
			})
		mcp.AddTool(s, &mcp.Tool{Name: "create_rollback", Description: "Plan an environment back to its previous successful release, saying why. Data-bearing resources and services stay as they are; the plan lists what it leaves. Waits until it's planned. Nothing changes until someone approves it.", Annotations: destructive()},
			func(ctx context.Context, _ *mcp.CallToolRequest, in createRollbackInput) (*mcp.CallToolResult, *client.Plan, error) {
				p, err := find(ctx, in.Project)
				if err != nil {
					return nil, nil, err
				}
				plan, err := c.CreateRollback(ctx, p.ID, client.NewRollback{Environment: in.Environment, Reason: in.Reason, DeploymentID: in.DeploymentID})
				if err != nil {
					return nil, nil, err
				}
				plan, err = c.WaitPlan(ctx, plan.ID)
				return nil, plan, err
			})
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
