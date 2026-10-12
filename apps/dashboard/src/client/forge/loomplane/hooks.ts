/**
 * Typed hooks over the operation manifest.
 *
 * Generated. Each line is a binding, not an implementation.
 *
 * The bodies live in @forge-go/client-core, so a runtime defect is fixed by
 * upgrading that package rather than by regenerating this one.
 */

import { query, mutation } from '@forge-go/client-core';
import { ops } from './ops';

import type { Analysis, AnalysisGraph, AnalysisList, BindingList, ConnectedRepositoryList, Connection, ConnectionList, Deployment, DeploymentList, Environment, EnvironmentConnections, EnvironmentList, GitHubInstallation, GitHubStatus, InfrastructureStatus, ManifestDraft, OrgInvitationList, OrgMemberList, Plan, PlanList, Project, ProjectList, ProviderList, PublicConfig, Resolution, Revision, RevisionList, Session, Source, SourceList, Variable, VariableList } from './types';

export const useAnalysesGet = query<Analysis>(ops['analyses.get']);
export const useAnalysesGraph = query<AnalysisGraph>(ops['analyses.graph']);
export const useConnectionsList = query<ConnectionList>(ops['connections.list']);
export const useConnectionsCreate = mutation<Connection, Connection>(ops['connections.create']);
export const useConnectionsGet = query<Connection>(ops['connections.get']);
export const useConnectionsDelete = mutation(ops['connections.delete']);
export const useConnectionsCheck = mutation<Connection, Connection>(ops['connections.check']);
export const useConnectionsReplace = mutation<Connection, Connection>(ops['connections.replace']);
export const useDeploymentsGet = query<Deployment>(ops['deployments.get']);
export const useGithubStatus = query<GitHubStatus>(ops['github.status']);
export const useGithubConnect = mutation<GitHubInstallation, GitHubInstallation>(ops['github.connect']);
export const useGithubDisconnect = mutation(ops['github.disconnect']);
export const useGithubRepositories = query<ConnectedRepositoryList>(ops['github.repositories']);
export const useInfrastructureGet = query<InfrastructureStatus>(ops['infrastructure.get']);
export const useInvitationsList = query<OrgInvitationList>(ops['invitations.list']);
export const useInvitationsCreate = mutation(ops['invitations.create']);
export const useMembersList = query<OrgMemberList>(ops['members.list']);
export const usePlansGet = query<Plan>(ops['plans.get']);
export const usePlansApprove = mutation<Plan, Plan>(ops['plans.approve']);
export const useDeploymentsForPlan = query<Deployment>(ops['deployments.forPlan']);
export const usePlansReject = mutation<Plan, Plan>(ops['plans.reject']);
export const useProjectsList = query<ProjectList>(ops['projects.list']);
export const useProjectsCreate = mutation<Project, Project>(ops['projects.create']);
export const useProjectsGet = query<Project>(ops['projects.get']);
export const useProjectsUpdate = mutation<Project, Project>(ops['projects.update']);
export const useProjectsDelete = mutation(ops['projects.delete']);
export const useAnalysesList = query<AnalysisList>(ops['analyses.list']);
export const useAnalysesCreate = mutation<AnalysisList, Analysis>(ops['analyses.create']);
export const useBindingsList = query<BindingList>(ops['bindings.list']);
export const useDeploymentsList = query<DeploymentList>(ops['deployments.list']);
export const useEnvironmentsList = query<EnvironmentList>(ops['environments.list']);
export const useEnvironmentsCreate = mutation<Environment, Environment>(ops['environments.create']);
export const useEnvironmentsDelete = mutation(ops['environments.delete']);
export const useEnvironmentConnectionsGet = query<EnvironmentConnections>(ops['environmentConnections.get']);
export const useEnvironmentConnectionsChoose = mutation(ops['environmentConnections.choose']);
export const useManifestsCurrent = query<Revision>(ops['manifests.current']);
export const useManifestsSave = mutation<Revision, Revision>(ops['manifests.save']);
export const useManifestsDraft = query<ManifestDraft>(ops['manifests.draft']);
export const useManifestsHistory = query<RevisionList>(ops['manifests.history']);
export const usePlansList = query<PlanList>(ops['plans.list']);
export const usePlansCreate = mutation<Plan, Plan>(ops['plans.create']);
export const useSourcesList = query<SourceList>(ops['sources.list']);
export const useSourcesCreate = mutation<Source, Source>(ops['sources.create']);
export const useSourcesUpdate = mutation<Source, Source>(ops['sources.update']);
export const useSourcesDelete = mutation(ops['sources.delete']);
export const useProjectVariablesList = query<VariableList>(ops['projectVariables.list']);
export const useProjectVariablesCreate = mutation<Variable, Variable>(ops['projectVariables.create']);
export const useVariablesResolve = query<Resolution>(ops['variables.resolve']);
export const useProvidersList = query<ProviderList>(ops['providers.list']);
export const usePublicConfigGet = query<PublicConfig>(ops['publicConfig.get']);
export const useSessionGet = query<Session>(ops['session.get']);
export const useVariablesList = query<VariableList>(ops['variables.list']);
export const useVariablesCreate = mutation<Variable, Variable>(ops['variables.create']);
export const useVariablesUpdate = mutation<Variable, Variable>(ops['variables.update']);
export const useVariablesDelete = mutation(ops['variables.delete']);
