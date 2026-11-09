// Package publicplan projects saved plans for review without execution authority.

package publicplan

import (
	"slices"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/gateway"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/discovery"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

type Plan struct {
	PlacementGroups   []PlacementGroupReview    `json:"placement_groups,omitempty"`
	CloudSyncID       string                    `json:"cloud_sync_id,omitempty"`
	Catalog           bool                      `json:"catalog"`
	Kind              string                    `json:"kind"`
	From              string                    `json:"from_environment"`
	FromEnvironmentID string                    `json:"from_environment_id"`
	Rollback          *planning.Rollback        `json:"rollback"`
	Leftovers         []planning.Leftover       `json:"leftovers"`
	Artifacts         []planning.ArtifactChoice `json:"artifacts"`
	ID                string                    `json:"id" forge:"id"`
	OrgID             string                    `json:"org_id"`
	ProjectID         string                    `json:"project_id"`
	EnvironmentID     string                    `json:"environment_id"`
	Environment       string                    `json:"environment"`
	Status            string                    `json:"status"`
	Revision          string                    `json:"revision"`
	Sources           []planning.PlanSource     `json:"sources"`
	Services          []string                  `json:"services,omitempty"`
	ManifestID        string                    `json:"manifest_id"`
	ManifestRevision  int                       `json:"manifest_revision"`
	Actions           []Action                  `json:"actions"`
	Problems          []planning.Finding        `json:"problems"`
	Warnings          []planning.Finding        `json:"warnings"`
	Summary           planning.Summary          `json:"summary"`
	Executable        bool                      `json:"executable"`
	Connections       map[string]string         `json:"connections"`
	Release           *planning.Release         `json:"release,omitempty"`
	Error             string                    `json:"error"`
	CreatedBy         string                    `json:"created_by"`
	CreatedAt         time.Time                 `json:"created_at"`
	PlannedAt         *time.Time                `json:"planned_at"`
	ExpiresAt         *time.Time                `json:"expires_at"`
	DecidedBy         string                    `json:"decided_by"`
	DecidedAt         *time.Time                `json:"decided_at"`
	DecisionNote      string                    `json:"decision_note"`
}

type CloudAdoption struct {
	BindingID  string `json:"binding_id"`
	ExternalID string `json:"external_id"`
	Revision   string `json:"revision"`
	PlanID     string `json:"plan_id"`
}

func projectAdoption(v *planning.CloudAdoption) *CloudAdoption {
	if v == nil {
		return nil
	}
	return &CloudAdoption{BindingID: v.BindingID, ExternalID: v.ExternalID, Revision: v.Revision, PlanID: v.PlanID}
}

type Action struct {
	CloudAdoption       *CloudAdoption     `json:"cloud_adoption,omitempty"`
	BackupEveryHours    int                `json:"backup_every_hours,omitempty"`
	Compensation        *CloudCompensation `json:"compensation,omitempty"`
	Routes              []cloudinfra.Route `json:"routes,omitempty"`
	AdoptCloudPlacement bool               `json:"adopt_cloud_placement,omitempty"`
	ID                  string             `json:"id"`
	Op                  planning.Op        `json:"op"`
	Subject             planning.Subject   `json:"subject"`
	Name                string             `json:"name"`
	Provider            string             `json:"provider"`
	ConnectionID        string             `json:"connection_id,omitempty"`
	Region              string             `json:"region,omitempty"`
	Engine              provider.Engine    `json:"engine"`
	Creates             string             `json:"creates"`
	Service             *ServiceConfig     `json:"service,omitempty"`
	Resource            *ResourceConfig    `json:"resource,omitempty"`
	Changes             []planning.Change  `json:"changes,omitempty"`
	Reason              string             `json:"reason"`
	DependsOn           []string           `json:"depends_on,omitempty"`
	Step                int                `json:"step"`
	CleanupTargets      []CleanupTarget    `json:"cleanup_targets,omitempty"`
}

type ServiceConfig struct {
	Worker                *provider.WorkerSettings             `json:"worker,omitempty"`
	TargetAccountIdentity string                               `json:"target_account_identity,omitempty"`
	CapacityMode          string                               `json:"capacity_mode,omitempty"`
	CapacityIntent        *CapacityIntent                      `json:"capacity_intent,omitempty"`
	CloudPlacement        *CloudPlacement                      `json:"cloud_placement,omitempty"`
	Catalog               *recipes.Recipe                      `json:"catalog,omitempty"`
	Provider              string                               `json:"provider"`
	Region                string                               `json:"region,omitempty"`
	Target                string                               `json:"target,omitempty"`
	TargetSettings        map[string]string                    `json:"target_settings,omitempty"`
	Endpoint              *manifest.Endpoint                   `json:"endpoint,omitempty"`
	HealthPath            string                               `json:"health_path,omitempty"`
	BuildTarget           *BuildTarget                         `json:"build_target,omitempty"`
	Workload              provider.Workload                    `json:"workload"`
	Execution             provider.Execution                   `json:"execution"`
	Service               string                               `json:"service"`
	Source                string                               `json:"source"`
	Repo                  string                               `json:"repo,omitempty"`
	CommitSHA             string                               `json:"commit_sha,omitempty"`
	Ecosystem             discovery.Ecosystem                  `json:"ecosystem"`
	Framework             string                               `json:"framework,omitempty"`
	Image                 string                               `json:"image,omitempty"`
	Context               string                               `json:"context,omitempty"`
	Build                 string                               `json:"build,omitempty"`
	Start                 string                               `json:"start,omitempty"`
	Port                  int                                  `json:"port,omitempty"`
	Scale                 *manifest.Scale                      `json:"scale,omitempty"`
	Size                  *manifest.Size                       `json:"size,omitempty"`
	Public                bool                                 `json:"public"`
	Internal              bool                                 `json:"internal,omitempty"`
	PrivateEgress         bool                                 `json:"private_egress,omitempty"`
	CloudSQLNetwork       *provider.CloudSQLApplicationNetwork `json:"cloud_sql_network,omitempty"`
	Env                   []planning.EnvLink                   `json:"env,omitempty"`
	Gateway               []gateway.Route                      `json:"gateway,omitempty"`
	DatabaseGateway       *planning.DatabaseGatewayConfig      `json:"database_gateway,omitempty"`
	Component             *ComponentConfig                     `json:"component,omitempty"`
	Farp                  *planning.FarpConfig                 `json:"farp,omitempty"`
	GPU                   *provider.GPUPlacement               `json:"gpu,omitempty"`
	Artifact              *planning.ReusedArtifact             `json:"artifact,omitempty"`
	ClusterDemand         *ClusterDemand                       `json:"cluster_demand,omitempty"`
}

type ResourceConfig struct {
	Settings         map[string]string               `json:"settings,omitempty"`
	EndpointPins     map[string]planning.EndpointPin `json:"endpoint_pins,omitempty"`
	AccountIdentity  string                          `json:"account_identity,omitempty"`
	ExistingResource *ExistingResourceConfig         `json:"existing_resource,omitempty"`
	Target           string                          `json:"target,omitempty"`
	TargetSettings   map[string]string               `json:"target_settings,omitempty"`
	Catalog          *recipes.Recipe                 `json:"catalog,omitempty"`
	Managed          *ManagedDatabaseConfig          `json:"managed,omitempty"`
	Provider         string                          `json:"provider"`
	Region           string                          `json:"region,omitempty"`
	Kind             string                          `json:"kind"`
	Version          string                          `json:"version,omitempty"`
	Composition      *CompositionConfig              `json:"composition,omitempty"`
	Component        *ComponentConfig                `json:"component,omitempty"`
	RoutablePorts    []int                           `json:"routable_ports,omitempty"`
	CloudPlacement   *CloudPlacement                 `json:"cloud_placement,omitempty"`
}

type CloudPlacement struct {
	AccountIdentity      string `json:"account_identity"`
	Allocation           string `json:"allocation,omitempty"`
	PoolID               string `json:"pool_id,omitempty"`
	PoolRevision         int64  `json:"pool_revision,omitempty"`
	ServerID             string `json:"server_id,omitempty"`
	ServerRevision       int64  `json:"server_revision,omitempty"`
	ConnectionGeneration int64  `json:"connection_generation,omitempty"`
	CPUMillis            int64  `json:"cpu_millis,omitempty"`
	MemoryBytes          int64  `json:"memory_bytes,omitempty"`
	GPUKind              string `json:"gpu_kind,omitempty"`
	GPUs                 int64  `json:"gpus,omitempty"`
	GrantRevision        int64  `json:"grant_revision,omitempty"`
	CloudID              string `json:"cloud_id"`
	CloudRevision        int64  `json:"cloud_revision"`
	AssignmentRevision   int64  `json:"assignment_revision"`
	Default              bool   `json:"default,omitempty"`
	TargetID             string `json:"target_id"`
	TargetRevision       int64  `json:"target_revision"`
	TargetName           string `json:"target_name"`
	Provider             string `json:"provider"`
	ConnectionID         string `json:"connection_id"`
	Region               string `json:"region,omitempty"`
	RegionSource         string `json:"region_source,omitempty"`
}

type ExistingResourceConfig struct {
	Incarnation      string `json:"incarnation"`
	AccountIdentity  string `json:"account_identity"`
	ResourceID       string `json:"resource_id"`
	EndpointID       string `json:"endpoint_id"`
	Revision         int64  `json:"revision"`
	EndpointRevision int    `json:"endpoint_revision"`
	ExternalID       string `json:"external_id"`
}

type ManagedDatabaseConfig struct {
	AccountIdentity string                          `json:"account_identity"`
	Profile         provider.ManagedDatabaseProfile `json:"profile"`
	ConnectionID    string                          `json:"connection_id"`
	CloudSQLPSC     provider.CloudSQLSourceEndpoint `json:"cloud_sql_psc,omitempty,omitzero"`
}

type CompositionConfig struct {
	Kind         string                      `json:"kind"`
	Version      string                      `json:"version"`
	Revision     string                      `json:"revision"`
	Dependencies map[string]DependencyChoice `json:"dependencies,omitempty"`
	Outputs      []recipes.Output            `json:"outputs,omitempty"`
	Secrets      []recipes.GeneratedSecret   `json:"secrets,omitempty"`
}

type DependencyChoice struct {
	ExistingResource *ExistingResourceConfig `json:"existing_resource,omitempty"`
	Mode             string                  `json:"mode"`
	Resource         string                  `json:"resource,omitempty"`
	External         string                  `json:"external,omitempty"`
	Kind             string                  `json:"kind,omitempty"`
}

type BuildTarget struct {
	BuilderCloud     string            `json:"builder_cloud,omitempty"`
	Builder          string            `json:"builder,omitempty"`
	Registry         string            `json:"registry,omitempty"`
	Image            string            `json:"image,omitempty"`
	BuilderSettings  map[string]string `json:"builder_settings,omitempty"`
	RegistrySettings map[string]string `json:"registry_settings,omitempty"`
}

// CapacityDemand is the project-visible capacity request. Server-owned location
// policy and runtime authority stay in the stored planning demand.
type CapacityDemand struct {
	OrgID          string                    `json:"org_id"`
	CloudID        string                    `json:"cloud_id"`
	PoolID         string                    `json:"pool_id"`
	TargetID       string                    `json:"target_id"`
	ProjectID      string                    `json:"project_id"`
	EnvironmentID  string                    `json:"environment_id"`
	PlanID         string                    `json:"plan_id"`
	Service        string                    `json:"service"`
	Architecture   string                    `json:"architecture"`
	StickyServerID string                    `json:"sticky_server_id"`
	Resources      cloudinfra.ResourceVector `json:"resources"`
	LocalData      bool                      `json:"local_data"`
	PolicyRevision int64                     `json:"policy_revision"`
	IntentHash     string                    `json:"intent_hash"`
}

type CapacityIntent struct {
	Demand             CapacityDemand                 `json:"demand"`
	Template           cloudinfra.PoolExpansionPolicy `json:"template"`
	TemplateHash       string                         `json:"template_hash"`
	CommitSHA          string                         `json:"commit_sha"`
	AssignmentRevision int64                          `json:"assignment_revision"`
	CloudRevision      int64                          `json:"cloud_revision"`
	TargetRevision     int64                          `json:"target_revision"`
	GrantRevision      int64                          `json:"grant_revision"`
	TargetName         string                         `json:"target_name"`
}

type CloudCompensation struct {
	PlanID            string             `json:"plan_id"`
	Routes            []cloudinfra.Route `json:"routes,omitempty"`
	ActionID          string             `json:"action_id"`
	Retained          bool               `json:"retained"`
	ServerAttempt     bool               `json:"server_attempt"`
	OriginalCompleted bool               `json:"original_completed"`
}

type ComponentConfig struct {
	Kind         string                      `json:"kind"`
	Resource     string                      `json:"resource"`
	Revision     string                      `json:"revision"`
	Component    recipes.ResolvedComponent   `json:"component"`
	Dependencies map[string]DependencyChoice `json:"dependencies,omitempty"`
	Protection   recipes.Protection          `json:"protection"`
	GPU          *provider.GPUPlacement      `json:"gpu,omitempty"`
	Inputs       string                      `json:"inputs,omitempty"`
	Settings     map[string]string           `json:"settings,omitempty"`
}

func projectPlan(v *planning.Plan) *Plan {
	if v == nil {
		return nil
	}
	return &Plan{PlacementGroups: placementGroupReviews(v.PlacementGroups), CloudSyncID: v.CloudSyncID, Catalog: v.Catalog, Kind: v.Kind, From: v.From, FromEnvironmentID: v.FromEnvironmentID, Rollback: v.Rollback, Leftovers: append([]planning.Leftover{}, v.Leftovers...), Artifacts: append([]planning.ArtifactChoice{}, v.Artifacts...), ID: v.ID, OrgID: v.OrgID, ProjectID: v.ProjectID, EnvironmentID: v.EnvironmentID, Environment: v.Environment, Status: v.Status, Revision: v.Revision, Sources: append([]planning.PlanSource{}, v.Sources...), Services: slices.Clone(v.Services), ManifestID: v.ManifestID, ManifestRevision: v.ManifestRevision, Actions: actions(v.Actions), Problems: append([]planning.Finding{}, v.Problems...), Warnings: append([]planning.Finding{}, v.Warnings...), Summary: v.Summary, Executable: v.Executable, Connections: v.Connections, Release: v.Release, Error: v.Error, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt, PlannedAt: v.PlannedAt, ExpiresAt: v.ExpiresAt, DecidedBy: v.DecidedBy, DecidedAt: v.DecidedAt, DecisionNote: v.DecisionNote}
}

func projectAction(v *planning.Action) *Action {
	if v == nil {
		return nil
	}
	return &Action{CloudAdoption: projectAdoption(v.CloudAdoption), BackupEveryHours: v.BackupEveryHours, Compensation: projectCloudCompensation(v.Compensation), Routes: v.Routes, AdoptCloudPlacement: v.AdoptCloudPlacement, ID: v.ID, Op: v.Op, Subject: v.Subject, Name: v.Name, Provider: v.Provider, ConnectionID: v.ConnectionID, Region: v.Region, Engine: v.Engine, Creates: v.Creates, Service: projectServiceConfig(v.Service), Resource: projectResourceConfig(v.Resource), Changes: v.Changes, Reason: v.Reason, DependsOn: v.DependsOn, Step: v.Step, CleanupTargets: CleanupTargets(v.CleanupTargets)}
}

func projectServiceConfig(v *planning.ServiceConfig) *ServiceConfig {
	if v == nil {
		return nil
	}
	return &ServiceConfig{Worker: projectWorkerSettings(v.Worker), TargetAccountIdentity: v.TargetAccountIdentity, CapacityMode: v.CapacityMode, CapacityIntent: projectCapacityIntent(v.CapacityIntent), CloudPlacement: projectCloudPlacement(v.CloudPlacement), Catalog: v.Catalog, Provider: v.Provider, Region: v.Region, Target: v.Target, TargetSettings: settings(v.TargetSettings), Endpoint: v.Endpoint, HealthPath: v.HealthPath, BuildTarget: projectBuildTarget(v.BuildTarget), Workload: v.Workload, Execution: v.Execution, Service: v.Service, Source: v.Source, Repo: v.Repo, CommitSHA: v.CommitSHA, Ecosystem: v.Ecosystem, Framework: v.Framework, Image: v.Image, Context: v.Context, Build: v.Build, Start: v.Start, Port: v.Port, Scale: v.Scale, Size: v.Size, Public: v.Public, Internal: v.Internal, PrivateEgress: v.PrivateEgress, CloudSQLNetwork: v.CloudSQLNetwork, Env: v.Env, Gateway: v.Gateway, DatabaseGateway: v.DatabaseGateway, Component: projectComponentConfig(v.Component), Farp: v.Farp, GPU: v.GPU, Artifact: v.Artifact, ClusterDemand: clusterDemand(v.ClusterDemand)}
}

func projectResourceConfig(v *planning.ResourceConfig) *ResourceConfig {
	if v == nil {
		return nil
	}
	return &ResourceConfig{Settings: edgeResourceSettings(v.Settings), EndpointPins: edgeEndpointPins(v.EndpointPins), AccountIdentity: v.AccountIdentity, ExistingResource: projectExistingResourceConfig(v.ExistingResource), Target: v.Target, TargetSettings: settings(v.TargetSettings), Catalog: v.Catalog, Managed: projectManagedDatabaseConfig(v.Managed), Provider: v.Provider, Region: v.Region, Kind: v.Kind, Version: v.Version, Composition: projectCompositionConfig(v.Composition), Component: projectComponentConfig(v.Component), RoutablePorts: v.RoutablePorts, CloudPlacement: projectCloudPlacement(v.CloudPlacement)}
}

func projectCloudPlacement(v *planning.CloudPlacement) *CloudPlacement {
	if v == nil {
		return nil
	}
	return &CloudPlacement{AccountIdentity: v.AccountIdentity, Allocation: v.Allocation, PoolID: v.PoolID, PoolRevision: v.PoolRevision, ServerID: v.ServerID, ServerRevision: v.ServerRevision, ConnectionGeneration: v.ConnectionGeneration, CPUMillis: v.CPUMillis, MemoryBytes: v.MemoryBytes, GPUKind: v.GPUKind, GPUs: v.GPUs, GrantRevision: v.GrantRevision, CloudID: v.CloudID, CloudRevision: v.CloudRevision, AssignmentRevision: v.AssignmentRevision, Default: v.Default, TargetID: v.TargetID, TargetRevision: v.TargetRevision, TargetName: v.TargetName, Provider: v.Provider, ConnectionID: v.ConnectionID, Region: v.Region, RegionSource: v.RegionSource}
}

func projectExistingResourceConfig(v *planning.ExistingResourceConfig) *ExistingResourceConfig {
	if v == nil {
		return nil
	}
	return &ExistingResourceConfig{Incarnation: v.Incarnation, AccountIdentity: v.AccountIdentity, ResourceID: v.ResourceID, EndpointID: v.EndpointID, Revision: v.Revision, EndpointRevision: v.EndpointRevision, ExternalID: v.ExternalID}
}

func projectManagedDatabaseConfig(v *planning.ManagedDatabaseConfig) *ManagedDatabaseConfig {
	if v == nil {
		return nil
	}
	return &ManagedDatabaseConfig{AccountIdentity: v.AccountIdentity, Profile: v.Profile, ConnectionID: v.ConnectionID, CloudSQLPSC: v.CloudSQLPSC}
}

func projectCompositionConfig(v *planning.CompositionConfig) *CompositionConfig {
	if v == nil {
		return nil
	}
	return &CompositionConfig{Kind: v.Kind, Version: v.Version, Revision: v.Revision, Dependencies: dependencies(v.Dependencies), Outputs: v.Outputs, Secrets: v.Secrets}
}

func projectDependencyChoice(v *planning.DependencyChoice) *DependencyChoice {
	if v == nil {
		return nil
	}
	return &DependencyChoice{ExistingResource: projectExistingResourceConfig(v.ExistingResource), Mode: v.Mode, Resource: v.Resource, External: v.External, Kind: v.Kind}
}

func projectBuildTarget(v *planning.BuildTarget) *BuildTarget {
	if v == nil {
		return nil
	}
	return &BuildTarget{BuilderCloud: v.BuilderCloud, Builder: v.Builder, Registry: v.Registry, Image: v.Image, BuilderSettings: settings(v.BuilderSettings), RegistrySettings: settings(v.RegistrySettings)}
}

func projectCapacityIntent(v *planning.CapacityIntent) *CapacityIntent {
	if v == nil {
		return nil
	}
	d := v.Demand
	demand := CapacityDemand{OrgID: d.OrgID, CloudID: d.CloudID, PoolID: d.PoolID, TargetID: d.TargetID, ProjectID: d.ProjectID, EnvironmentID: d.EnvironmentID, PlanID: d.PlanID, Service: d.Service, Architecture: d.Architecture, StickyServerID: d.StickyServerID, Resources: d.Resources, LocalData: d.LocalData, PolicyRevision: d.PolicyRevision, IntentHash: d.IntentHash}
	return &CapacityIntent{Demand: demand, Template: v.Template, TemplateHash: v.TemplateHash, CommitSHA: v.CommitSHA, AssignmentRevision: v.AssignmentRevision, CloudRevision: v.CloudRevision, TargetRevision: v.TargetRevision, GrantRevision: v.GrantRevision, TargetName: v.TargetName}
}

func projectCloudCompensation(v *planning.CloudCompensation) *CloudCompensation {
	if v == nil {
		return nil
	}
	return &CloudCompensation{PlanID: v.PlanID, Routes: v.Routes, ActionID: v.ActionID, Retained: v.Retained, ServerAttempt: v.ServerAttempt, OriginalCompleted: v.OriginalCompleted}
}

func projectComponentConfig(v *planning.ComponentConfig) *ComponentConfig {
	if v == nil {
		return nil
	}
	return &ComponentConfig{Kind: v.Kind, Resource: v.Resource, Revision: v.Revision, Component: v.Component, Dependencies: dependencies(v.Dependencies), Protection: v.Protection, GPU: v.GPU, Inputs: v.Inputs, Settings: v.Settings}
}

// Project preserves the original opaque revision. Only the stored plan can execute.
func Project(v *planning.Plan) *Plan { return projectPlan(v) }
func actions(in []planning.Action) []Action {
	out := make([]Action, len(in))
	for i := range in {
		out[i] = *projectAction(&in[i])
	}
	return out
}
func dependencies(in map[string]planning.DependencyChoice) map[string]DependencyChoice {
	if in == nil {
		return nil
	}
	out := make(map[string]DependencyChoice, len(in))
	for k, v := range in {
		out[k] = *projectDependencyChoice(&v)
	}
	return out
}
func settings(in map[string]string) map[string]string {
	return planning.PublicReviewedSettings(in)
}

// CleanupTarget explains the scope and uncertainty without journal or capture IDs.
type CleanupTarget struct {
	Kind             string          `json:"kind"`
	Service          string          `json:"service"`
	PlanID           string          `json:"plan_id"`
	DeploymentID     string          `json:"deployment_id"`
	Namespace        string          `json:"namespace"`
	Objects          []CleanupObject `json:"objects"`
	UnresolvedWrites int             `json:"unresolved_writes"`
	IdentityCurrent  bool            `json:"identity_current"`
}
type CleanupObject struct {
	Resource  string `json:"resource"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

func CleanupTargets(in []planning.ManagedCleanupTarget) []CleanupTarget {
	if in == nil {
		return nil
	}
	out := make([]CleanupTarget, 0, len(in))
	for _, t := range in {
		v := CleanupTarget{Kind: string(t.Kind), Service: t.Scope.Service, PlanID: t.Scope.Plan, DeploymentID: t.Scope.Deployment, Namespace: t.Scope.Namespace, UnresolvedWrites: len(t.Unresolved), IdentityCurrent: t.IdentityCurrent, Objects: []CleanupObject{}}
		for _, a := range t.Accepted {
			v.Objects = append(v.Objects, CleanupObject{Resource: a.Request.Resource, Name: a.Request.Name, Namespace: a.Request.Namespace})
		}
		out = append(out, v)
	}
	return out
}

type ClusterDemand struct {
	Mode    string                               `json:"mode"`
	Maximum cloudinfra.ClusterWorkloadVector     `json:"maximum"`
	Profile *cloudinfra.ClusterSchedulingProfile `json:"profile,omitempty"`
}

func clusterDemand(v *planning.ClusterWorkloadDemand) *ClusterDemand {
	if v == nil {
		return nil
	}
	return &ClusterDemand{Mode: v.Mode, Maximum: v.Input.Maximum, Profile: v.Profile}
}
