package agentops

import (
	"slices"
	"strings"
	"sync"
)

// Where the built-in operations are offered.
const (
	agentAndMCP    = SurfaceAgent | SurfaceMCP
	agentAndDeploy = SurfaceAgent | SurfaceMCPDeploy
)

var builtins = sync.OnceValues(func() ([]Definition, map[string]Definition) {
	list := []Definition{
		// Reads, offered over MCP too. Descriptions are loomctl mcp's.
		Define[NoInput]("whoami", "Who the API key acts as, in which organization, and the permissions it has.", RiskRead, ReadOnly(), On(agentAndMCP)),
		Define[NoInput]("list_projects", "The organization's projects.", RiskRead, ReadOnly(), On(agentAndMCP)),
		Define[ProjectInput]("get_manifest", "The project's current loomplane.yaml.", RiskRead, ReadOnly(), On(agentAndMCP)),
		Define[ProjectInput]("draft_manifest", "A drafted loomplane.yaml from the project's latest analysis. It isn't saved.", RiskRead, ReadOnly(), On(agentAndMCP)),
		Define[ProjectInput]("list_plans", "The project's plans, newest first.", RiskRead, ReadOnly(), On(agentAndMCP)),
		Define[PlanInput]("get_plan", "A plan: its actions step by step, the problems that block it, its warnings, and its revision.", RiskRead, ReadOnly(), On(agentAndMCP)),
		Define[ProjectInput]("list_deployments", "The project's deployments, newest first.", RiskRead, ReadOnly(), On(agentAndMCP)),
		Define[DeploymentInput]("get_deployment", "A deployment, step by step, with each step's status and message.", RiskRead, ReadOnly(), On(agentAndMCP)),
		Define[PlanInput]("deployment_for_plan", "The deployment an approved plan started.", RiskRead, ReadOnly(), On(agentAndMCP)),
		Define[ProjectInput]("status", "What runs each of the project's services and resources in each environment, with its address and any drift.", RiskRead, ReadOnly(), On(agentAndMCP)),
		Define[ProjectInput]("list_variables", "The project's variables. A secret's value is never shown.", RiskRead, ReadOnly(), On(agentAndMCP)),
		Define[ReleaseWatchInput]("get_release_watch", "How a deployment is watched after it finishes: its status and verdict, the verdict on each changed service, the services it can't watch, and the rollback plan if it breached. A deployment with no watch answers not found. Unverified means missing evidence, so ask a person.", RiskRead, ReadOnly(), On(agentAndMCP)),

		// Changes, offered over MCP too.
		Define[AnalyzeInput]("analyze", "Read the project's repositories, or the ones named, and find their services, packages and resources. One analysis per repository; waits until each is done.", RiskChange, On(agentAndMCP)),
		Define[SaveManifestInput]("save_manifest", "Save a new revision of the project's loomplane.yaml. A manifest that doesn't parse is refused with every problem and its line.", RiskChange, On(agentAndMCP)),
		Define[CreatePlanInput]("create_plan", "Plan the project's current manifest for an environment: what deploying it would do. With from, it promotes what that environment runs instead. Waits until it's planned. Nothing changes until someone approves it.", RiskChange, On(agentAndMCP)),
		Define[RejectInput]("reject_plan", "Reject a plan. Nothing it says happens.", RiskChange, On(agentAndMCP)),
		Define[SetVariableInput]("set_variable", "Set a project variable. Services get it on their next deployment.", RiskChange, FromInput(), On(agentAndMCP)),

		// Offered over MCP only with --allow-deploy.
		Define[CreateRollbackInput]("create_rollback", "Plan an environment back to its previous successful release, saying why. Data-bearing resources and services stay as they are; the plan lists what it leaves. Waits until it's planned. Nothing changes until someone approves it.", RiskChange, Destructive(), On(agentAndDeploy)),
		Define[ApproveInput]("approve_plan", "Approve a plan at the revision you reviewed, which deploys it to the cloud. Read it with get_plan first.", RiskDeployNonProduction, FromInput(), Destructive(), On(agentAndDeploy)),
		Define[DeploymentInput]("retry_deployment", "Retry a failed deployment from the step that failed, with the plan that was approved.", RiskDeployNonProduction, FromInput(), Destructive(), On(agentAndDeploy)),

		// Reads for the agent only.
		Define[NoInput]("github_status", "Whether Loomplane has a GitHub App, and the GitHub accounts connected to it.", RiskRead, ReadOnly()),
		Define[NoInput]("list_repositories", "The GitHub repositories Loomplane can read through the connected accounts.", RiskRead, ReadOnly()),
		Define[EnvironmentInput]("deployed", "What each of the project's repositories runs in an environment: the commit, the plan that deployed it, and when.", RiskRead, ReadOnly()),
		Define[DeploymentLogsInput]("deployment_logs", "The last lines of a deployment's log.", RiskRead, ReadOnly()),
		Define[DriftInput]("drift", "The project's services and resources that have drifted from what Loomplane deployed, in one environment or every one.", RiskRead, ReadOnly()),
		Define[BindingMetricsInput]("binding_metrics", "A service's recent traffic in an environment: requests, errors, error rate and latency, or why they can't be read.", RiskRead, ReadOnly()),
		Define[DeploymentInput]("canary_report", "A deployment's canary: its steps, and the verdict on each service with the reason.", RiskRead, ReadOnly()),
		Define[RecentCanariesInput]("recent_canaries", "An environment's recent deployments that ran a canary, newest first, with their verdicts.", RiskRead, ReadOnly()),
		Define[RollbackTargetInput]("rollback_target", "What rolling an environment back would do: the deployment it undoes and the release it restores, or why it can't.", RiskRead, ReadOnly()),
		Define[NoInput]("list_teams", "List the workspace's teams: name, slug, description and how many members.", RiskRead, ReadOnly()),

		// Changes for the agent only.
		Define[CreateProjectInput]("create_project", "Create a project, with its production and preview environments, from a repository if you name one.", RiskChange),
		Define[CreateEnvironmentInput]("create_environment", "Add an environment to a project.", RiskChange),
		Define[AddSourceInput]("add_source", "Attach another repository to a project, as one of its sources.", RiskChange),

		// Requests to a person, answered outside the run.
		Define[AskPersonInput]("ask_person", "Ask a person a question and wait for the answer. Ask when you can't infer a choice.", RiskSecretOrExternal, External()),
		Define[RequestSecretInput]("request_secret", "Ask a person to type a secret variable's value into Loomplane. You never see the value.", RiskSecretOrExternal, External()),
		Define[RequestGitHubInput]("request_github_connection", "Ask a person to connect the GitHub account that owns a repository, so Loomplane can read it.", RiskSecretOrExternal, External()),
		Define[RequestConnectionInput]("request_cloud_connection", "Ask a person to connect a cloud account.", RiskSecretOrExternal, External()),
	}
	slices.SortFunc(list, func(a, b Definition) int { return strings.Compare(a.Name, b.Name) })
	byName := make(map[string]Definition, len(list))
	for _, d := range list {
		byName[d.Name] = d
	}
	return list, byName
})

// All returns every built-in operation, sorted by name. The slice is the
// caller's.
func All() []Definition {
	list, _ := builtins()
	return slices.Clone(list)
}

// Lookup returns the built-in operation called name.
func Lookup(name string) (Definition, bool) {
	_, byName := builtins()
	d, ok := byName[name]
	return d, ok
}
