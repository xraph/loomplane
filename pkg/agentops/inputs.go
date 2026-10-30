package agentops

// Inputs. The jsonschema descriptions are what the model reads. The ones
// loomctl mcp served before this package existed keep their text exactly.

// NoInput is the input of an operation that takes none.
type NoInput struct{}

// ProjectInput names a project.
type ProjectInput struct {
	Project string `json:"project" jsonschema:"the project's slug, name or ID"`
}

// PlanInput names a plan.
type PlanInput struct {
	PlanID string `json:"plan_id" jsonschema:"the plan's ID"`
}

// DeploymentInput names a deployment.
type DeploymentInput struct {
	DeploymentID string `json:"deployment_id" jsonschema:"the deployment's ID"`
}

// AnalyzeInput is analyze's input.
type AnalyzeInput struct {
	Project string   `json:"project" jsonschema:"the project's slug, name or ID"`
	Sources []string `json:"sources,omitempty" jsonschema:"the repositories to analyze, by alias; every one of the project's if empty"`
	Ref     string   `json:"ref,omitempty" jsonschema:"a branch, tag or commit, for one repository only; its tracked branch if empty"`
}

// SaveManifestInput is save_manifest's input.
type SaveManifestInput struct {
	Project string `json:"project" jsonschema:"the project's slug, name or ID"`
	YAML    string `json:"yaml" jsonschema:"the whole loomplane.yaml; the schema is the resource loomplane://schema/loomplane.yaml"`
}

// CreatePlanInput is create_plan's input.
type CreatePlanInput struct {
	Project     string   `json:"project" jsonschema:"the project's slug, name or ID"`
	Environment string   `json:"environment" jsonschema:"the environment to plan for, such as production"`
	Scope       []string `json:"scope,omitempty" jsonschema:"the repositories the plan moves, by alias; every one if empty, and the rest stay as deployed"`
	From        string   `json:"from,omitempty" jsonschema:"An environment to promote from, such as staging: the plan pins the commits it runs"`
}

// ApproveInput is approve_plan's input. It names no team: an approval
// request always goes to the project's team.
type ApproveInput struct {
	PlanID   string `json:"plan_id" jsonschema:"the plan's ID"`
	Revision string `json:"revision" jsonschema:"the revision of the plan you reviewed, from get_plan; a plan that changed since comes back stale"`
	Note     string `json:"note,omitempty" jsonschema:"why, for the audit trail"`
}

// RejectInput is reject_plan's input.
type RejectInput struct {
	PlanID string `json:"plan_id" jsonschema:"the plan's ID"`
	Note   string `json:"note,omitempty" jsonschema:"why, for the audit trail"`
}

// SetVariableInput is set_variable's input.
type SetVariableInput struct {
	Project     string `json:"project" jsonschema:"the project's slug, name or ID"`
	Key         string `json:"key" jsonschema:"the variable's name, such as DATABASE_URL"`
	Value       string `json:"value" jsonschema:"its value"`
	Environment string `json:"environment,omitempty" jsonschema:"one environment; every environment if empty"`
	Service     string `json:"service,omitempty" jsonschema:"one service's graph ID; every service if empty"`
	Secret      bool   `json:"secret,omitempty" jsonschema:"keep it secret: write-only, and in the cloud's secret store"`
}

// CreateRollbackInput is create_rollback's input.
type CreateRollbackInput struct {
	Project      string `json:"project" jsonschema:"the project's slug, name or ID"`
	Environment  string `json:"environment" jsonschema:"the environment to roll back, such as production"`
	Reason       string `json:"reason" jsonschema:"why, for the audit trail and whoever approves it"`
	DeploymentID string `json:"deployment_id,omitempty" jsonschema:"the deployment to undo; it must be the environment's newest, which is the default"`
}

// ReleaseWatchInput is get_release_watch's input.
type ReleaseWatchInput struct {
	DeploymentID string `json:"deployment_id" jsonschema:"the deployment's ID"`
}

// CreateProjectInput is create_project's input.
type CreateProjectInput struct {
	Name       string `json:"name" jsonschema:"the project's name"`
	Repository string `json:"repository,omitempty" jsonschema:"its first repository: a GitHub URL, an SSH address or owner/name; none if empty"`
}

// CreateEnvironmentInput is create_environment's input.
type CreateEnvironmentInput struct {
	Project string `json:"project" jsonschema:"the project's slug, name or ID"`
	Name    string `json:"name" jsonschema:"the environment's name: lower case letters, digits and hyphens, such as staging"`
	Kind    string `json:"kind,omitempty" jsonschema:"production, staging, preview or development; picked from the name if empty"`
}

// AddSourceInput is add_source's input.
type AddSourceInput struct {
	Project    string `json:"project" jsonschema:"the project's slug, name or ID"`
	Repository string `json:"repository" jsonschema:"the repository: a GitHub URL or owner/name"`
	Alias      string `json:"alias,omitempty" jsonschema:"what the project calls it; picked from the repository's name if empty"`
	Branch     string `json:"branch,omitempty" jsonschema:"the branch analyses read; the repository's default branch if empty"`
}

// EnvironmentInput names one of a project's environments.
type EnvironmentInput struct {
	Project     string `json:"project" jsonschema:"the project's slug, name or ID"`
	Environment string `json:"environment" jsonschema:"the environment, such as production"`
}

// DriftInput is drift's input: a project, and one environment or all.
type DriftInput struct {
	Project     string `json:"project" jsonschema:"the project's slug, name or ID"`
	Environment string `json:"environment,omitempty" jsonschema:"one environment; every environment if empty"`
}

// DeploymentLogsInput is deployment_logs's input.
type DeploymentLogsInput struct {
	DeploymentID string `json:"deployment_id" jsonschema:"the deployment's ID"`
	Lines        int    `json:"lines,omitempty" jsonschema:"how many of the last lines to read; 200 if zero, at most 1000"`
}

// BindingMetricsInput is binding_metrics's input.
type BindingMetricsInput struct {
	Project     string `json:"project" jsonschema:"the project's slug, name or ID"`
	Environment string `json:"environment" jsonschema:"the environment, such as production"`
	Service     string `json:"service" jsonschema:"the service's graph ID"`
	Minutes     int    `json:"minutes,omitempty" jsonschema:"how many minutes back to read; 30 if zero, at most 1440"`
}

// RecentCanariesInput is recent_canaries's input.
type RecentCanariesInput struct {
	Project     string `json:"project" jsonschema:"the project's slug, name or ID"`
	Environment string `json:"environment" jsonschema:"the environment, such as production"`
	Limit       int    `json:"limit,omitempty" jsonschema:"how many deployments, newest first; 1 to 50"`
}

// RollbackTargetInput is rollback_target's input.
type RollbackTargetInput struct {
	Project      string `json:"project" jsonschema:"the project's slug, name or ID"`
	Environment  string `json:"environment" jsonschema:"the environment to roll back, such as production"`
	DeploymentID string `json:"deployment_id,omitempty" jsonschema:"the deployment to undo; it must be the environment's newest, which is the default"`
}

// The four requests to a person can name another team to hear about
// them. A team needs a reason: the request service refuses a team
// without one.

// AskPersonInput is ask_person's input.
type AskPersonInput struct {
	Question   string   `json:"question" jsonschema:"the question, as the person will read it"`
	Options    []string `json:"options,omitempty" jsonschema:"the answers to choose from, when it's a choice"`
	Project    string   `json:"project,omitempty" jsonschema:"the project it's about, by slug, name or ID"`
	Team       string   `json:"team,omitempty" jsonschema:"a team's slug from list_teams, when another team should hear about this"`
	TeamReason string   `json:"team_reason,omitempty" jsonschema:"why that team; required with team"`
}

// RequestSecretInput is request_secret's input.
type RequestSecretInput struct {
	Project     string `json:"project" jsonschema:"the project's slug, name or ID"`
	Key         string `json:"key" jsonschema:"the variable's name, such as DATABASE_URL"`
	Environment string `json:"environment,omitempty" jsonschema:"one environment; every environment if empty"`
	Service     string `json:"service,omitempty" jsonschema:"one service's graph ID; every service if empty"`
	Why         string `json:"why" jsonschema:"what needs it, for the person who types it"`
	Team        string `json:"team,omitempty" jsonschema:"a team's slug from list_teams, when another team should hear about this"`
	TeamReason  string `json:"team_reason,omitempty" jsonschema:"why that team; required with team"`
}

// RequestGitHubInput is request_github_connection's input.
type RequestGitHubInput struct {
	Repository string `json:"repository" jsonschema:"the repository Loomplane can't read: a GitHub URL or owner/name"`
	Why        string `json:"why" jsonschema:"what you need it for, for the person who connects it"`
	Team       string `json:"team,omitempty" jsonschema:"a team's slug from list_teams, when another team should hear about this"`
	TeamReason string `json:"team_reason,omitempty" jsonschema:"why that team; required with team"`
}

// RequestConnectionInput is request_cloud_connection's input.
type RequestConnectionInput struct {
	Cloud      string `json:"cloud" jsonschema:"the cloud to connect, such as aws, gcp, azure or digitalocean"`
	Why        string `json:"why" jsonschema:"what you need it for, for the person who connects it"`
	Team       string `json:"team,omitempty" jsonschema:"a team's slug from list_teams, when another team should hear about this"`
	TeamReason string `json:"team_reason,omitempty" jsonschema:"why that team; required with team"`
}
