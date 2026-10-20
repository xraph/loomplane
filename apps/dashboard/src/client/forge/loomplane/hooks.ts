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

import type { APIKeyList, ActivitySummary, Analysis, AnalysisGraph, AnalysisList, Backup, BackupList, BindingList, CloudList, ConnectedRepositoryList, Connection, ConnectionList, DeliveryList, DeployedList, Deployment, DeploymentList, Environment, EnvironmentConnections, EnvironmentList, GitHubInstallation, GitHubStatus, InfrastructureStatus, LogPage, ManifestDraft, NotificationList, OrgInvitationList, OrgMemberList, Plan, PlanList, Policy, PolicyList, Preferences, Project, ProjectList, ProjectView, ProviderList, PublicConfig, Resolution, Restore, RestoreList, RetentionPreview, Revision, RevisionList, Session, Snapshot, Source, SourceList, StorageVolumeList, Variable, VariableList, WebhookEndpointList } from './types';

export const useActivityGet = query<ActivitySummary>(ops['activity.get']);
export const useAnalysesGet = query<Analysis>(ops['analyses.get']);
export const useAnalysesGraph = query<AnalysisGraph>(ops['analyses.graph']);
export const useApiKeysList = query<APIKeyList>(ops['apiKeys.list']);
export const useApiKeysCreate = mutation(ops['apiKeys.create']);
export const useApiKeysRevoke = mutation(ops['apiKeys.revoke']);
export const useConnectionCloudsList = query<CloudList>(ops['connectionClouds.list']);
export const useConnectionsList = query<ConnectionList>(ops['connections.list']);
export const useConnectionsCreate = mutation<Connection, Connection>(ops['connections.create']);
export const useConnectionsGet = query<Connection>(ops['connections.get']);
export const useConnectionsDelete = mutation(ops['connections.delete']);
export const useConnectionsCheck = mutation<Connection, Connection>(ops['connections.check']);
export const useConnectionsReplace = mutation<Connection, Connection>(ops['connections.replace']);
export const useConnectionStorageList = query<StorageVolumeList>(ops['connectionStorage.list']);
export const useDeploymentsGet = query<Deployment>(ops['deployments.get']);
export const useDeploymentsLogs = query<LogPage>(ops['deployments.logs']);
export const useDeploymentsRetry = mutation<Deployment, Deployment>(ops['deployments.retry']);
export const useGithubStatus = query<GitHubStatus>(ops['github.status']);
export const useGithubConnect = mutation<GitHubInstallation, GitHubInstallation>(ops['github.connect']);
export const useGithubDisconnect = mutation(ops['github.disconnect']);
export const useGithubRepositories = query<ConnectedRepositoryList>(ops['github.repositories']);
export const useInfrastructureGet = query<InfrastructureStatus>(ops['infrastructure.get']);
export const useInvitationsList = query<OrgInvitationList>(ops['invitations.list']);
export const useInvitationsCreate = mutation(ops['invitations.create']);
export const useMembersList = query<OrgMemberList>(ops['members.list']);
export const useNotificationsList = query<NotificationList>(ops['notifications.list']);
export const useNotificationsPreferencesGet = query<Preferences>(ops['notifications.preferences.get']);
export const useNotificationsPreferencesUpdate = mutation(ops['notifications.preferences.update']);
export const useNotificationsRetry = mutation(ops['notifications.retry']);
export const useNotificationsWebhooksList = query<WebhookEndpointList>(ops['notifications.webhooks.list']);
export const useNotificationsWebhooksCreate = mutation(ops['notifications.webhooks.create']);
export const useNotificationsWebhooksDelete = mutation(ops['notifications.webhooks.delete']);
export const useNotificationsWebhooksDeliveries = query<DeliveryList>(ops['notifications.webhooks.deliveries']);
export const useNotificationsWebhooksRotate = mutation(ops['notifications.webhooks.rotate']);
export const useNotificationsRead = mutation(ops['notifications.read']);
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
export const useBackupPoliciesList = query<PolicyList>(ops['backupPolicies.list']);
export const useBackupPoliciesSet = mutation<Policy, Policy>(ops['backupPolicies.set']);
export const useBindingsList = query<BindingList>(ops['bindings.list']);
export const useDeploymentsList = query<DeploymentList>(ops['deployments.list']);
export const useEnvironmentsList = query<EnvironmentList>(ops['environments.list']);
export const useEnvironmentsCreate = mutation<Environment, Environment>(ops['environments.create']);
export const useEnvironmentsDelete = mutation(ops['environments.delete']);
export const useEnvironmentConnectionsGet = query<EnvironmentConnections>(ops['environmentConnections.get']);
export const useEnvironmentConnectionsChoose = mutation(ops['environmentConnections.choose']);
export const useDeployedList = query<DeployedList>(ops['deployed.list']);
export const useProjectsGraph = query<ProjectView>(ops['projects.graph']);
export const useManifestsCurrent = query<Revision>(ops['manifests.current']);
export const useManifestsSave = mutation<Revision, Revision>(ops['manifests.save']);
export const useManifestsDraft = query<ManifestDraft>(ops['manifests.draft']);
export const useManifestsEdit = mutation<Revision, Revision>(ops['manifests.edit']);
export const useManifestsHistory = query<RevisionList>(ops['manifests.history']);
export const usePlansList = query<PlanList>(ops['plans.list']);
export const usePlansCreate = mutation<Plan, Plan>(ops['plans.create']);
export const useServiceBackupsList = query<BackupList>(ops['serviceBackups.list']);
export const useServiceBackupsCreate = mutation<Backup, Backup>(ops['serviceBackups.create']);
export const useServiceRestoresList = query<RestoreList>(ops['serviceRestores.list']);
export const useSourcesList = query<SourceList>(ops['sources.list']);
export const useSourcesCreate = mutation<Source, Source>(ops['sources.create']);
export const useSourcesUpdate = mutation<Source, Source>(ops['sources.update']);
export const useSourcesDelete = mutation(ops['sources.delete']);
export const useProjectVariablesList = query<VariableList>(ops['projectVariables.list']);
export const useProjectVariablesCreate = mutation<Variable, Variable>(ops['projectVariables.create']);
export const useVariablesResolve = query<Resolution>(ops['variables.resolve']);
export const useProvidersList = query<ProviderList>(ops['providers.list']);
export const usePublicConfigGet = query<PublicConfig>(ops['publicConfig.get']);
export const useRealtimeEvents = query(ops['realtime.events']);
export const useRealtimeSnapshot = query<Snapshot>(ops['realtime.snapshot']);
export const useRealtimeWs = query(ops['realtime.ws']);
export const useServiceBackupsGet = query<Backup>(ops['serviceBackups.get']);
export const useServiceRestoresCreate = mutation<Restore, Restore>(ops['serviceRestores.create']);
export const useServiceBackupsPreviewRetention = query<RetentionPreview>(ops['serviceBackups.previewRetention']);
export const useServiceBackupsRetry = mutation<Backup, Backup>(ops['serviceBackups.retry']);
export const useServiceRestoresGet = query<Restore>(ops['serviceRestores.get']);
export const useServiceRestoresDelete = mutation(ops['serviceRestores.delete']);
export const useSessionGet = query<Session>(ops['session.get']);
export const useVariablesList = query<VariableList>(ops['variables.list']);
export const useVariablesCreate = mutation<Variable, Variable>(ops['variables.create']);
export const useVariablesUpdate = mutation<Variable, Variable>(ops['variables.update']);
export const useVariablesDelete = mutation(ops['variables.delete']);
