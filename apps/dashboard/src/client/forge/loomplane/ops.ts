import type { CodecRef } from './codec-runtime';
import { CODECS } from './codecs';

/**
 * Operation manifest.
 *
 * Generated. Entity identity was resolved in Go against the response schema, so
 * the runtime never has to guess which field identifies a record -- the class of
 * guess that, made wrong on a type carrying both an id and a tenant id, keys two
 * tenants' records to one cache entry.
 */

export interface OperationMeta {
  readonly method: string;
  readonly path: string;
  /** The entity this operation's cache contract is about. */
  readonly entity?: string;
  /**
   * The typename of the response document itself -- of its ELEMENTS when the
   * response is a bare array -- which is what indexes the entities table below.
   *
   * Not interchangeable with the entity field above. On a paginated read the
   * two differ: entity is 'Order' while rootType is 'PageOrder'. Normalizing
   * such a response against 'Order' would read Order's field edges against an
   * envelope's properties and descend into nothing.
   */
  readonly rootType?: string;
  /** Milliseconds the client considers this operation's result fresh. */
  readonly staleTime?: number;
  readonly provides: readonly string[];
  readonly invalidates: readonly string[];
  /**
   * Keys into securitySchemes below, so an AuthProvider can attach exactly
   * the credential this operation declared instead of blanketing every
   * request with every credential the document happens to carry.
   */
  readonly security?: readonly string[];
  /**
   * Schema id (a key into src/codecs.ts's CODECS table) that renames this
   * operation's JSON request body from its TypeScript shape to the wire shape.
   *
   * Present only when the endpoint's request body is application/json AND
   * resolves to a named component schema -- the same condition under which the
   * generated typed method sets RequestConfig.bodyCodec, because both come
   * from the same resolver.
   */
  readonly bodyCodec?: CodecRef;
  /** The same, for decoding a JSON response back into its TypeScript shape. */
  readonly responseCodec?: CodecRef;
}

/**
 * A row with no idField is a signpost, not a record: an envelope, or a hop
 * between two entities. It is walked for its fields and never stored.
 *
 * The property names below -- idField, and the KEYS of fields -- are the
 * client-side names this client's field naming produces, because that is what
 * the decoded payload actually carries. The VALUES of fields are typenames,
 * which are not field names and are never renamed.
 */
export interface EntityMeta {
  readonly idField?: string;
  readonly fields?: Readonly<Record<string, string>>;
}

/**
 * Every security scheme the document declares, described once.
 *
 * Normalized rather than inlined per operation, for the same reason
 * `entities` is its own table: repeating four fields across two hundred
 * operations is bundle weight for data that never varies. An operation
 * carries the keys; an AuthProvider looks them up here.
 */
export const securitySchemes = {
  'session': { type: 'http', scheme: 'bearer' },
  'bearerAuth': { type: 'http', scheme: 'bearer' },
  'session-cookie': { type: 'apiKey', in: 'cookie', name: 'auth_token' },
} as const;

export const ops = {
  'analyses.get': {
    method: 'GET',
    path: '/api/v1/analyses/{id}',
    entity: 'Analysis',
    rootType: 'Analysis',
    provides: ['Analysis:{id}'],
    invalidates: [],
    responseCodec: CODECS['Analysis'],
  },
  'analyses.graph': {
    method: 'GET',
    path: '/api/v1/analyses/{id}/graph',
    entity: 'AnalysisGraph',
    rootType: 'AnalysisGraph',
    provides: ['AnalysisGraph:{id}'],
    invalidates: [],
    responseCodec: CODECS['AnalysisGraph'],
  },
  'apiKeys.list': {
    method: 'GET',
    path: '/api/v1/api-keys',
    entity: 'APIKey',
    rootType: 'APIKeyList',
    provides: ['APIKey:{id}', 'APIKey[]'],
    invalidates: [],
    responseCodec: CODECS['APIKeyList'],
  },
  'apiKeys.create': {
    method: 'POST',
    path: '/api/v1/api-keys',
    rootType: 'CreatedKey',
    provides: [],
    invalidates: [],
    bodyCodec: CODECS['NewKeyBody'],
    responseCodec: CODECS['CreatedKey'],
  },
  'apiKeys.revoke': {
    method: 'DELETE',
    path: '/api/v1/api-keys/{id}',
    entity: 'APIKey',
    provides: [],
    invalidates: ['APIKey[]'],
  },
  'connectionClouds.list': {
    method: 'GET',
    path: '/api/v1/connection-clouds',
    provides: [],
    invalidates: [],
    responseCodec: CODECS['CloudList'],
  },
  'connections.list': {
    method: 'GET',
    path: '/api/v1/connections',
    entity: 'Connection',
    rootType: 'ConnectionList',
    provides: ['Connection:{id}', 'Connection[]'],
    invalidates: [],
    responseCodec: CODECS['ConnectionList'],
  },
  'connections.create': {
    method: 'POST',
    path: '/api/v1/connections',
    entity: 'Connection',
    rootType: 'Connection',
    provides: ['Connection:{id}'],
    invalidates: ['Connection[]'],
    bodyCodec: CODECS['CreateInputBody'],
    responseCodec: CODECS['Connection'],
  },
  'connections.get': {
    method: 'GET',
    path: '/api/v1/connections/{id}',
    entity: 'Connection',
    rootType: 'Connection',
    provides: ['Connection:{id}'],
    invalidates: [],
    responseCodec: CODECS['Connection'],
  },
  'connections.delete': {
    method: 'DELETE',
    path: '/api/v1/connections/{id}',
    entity: 'Connection',
    provides: [],
    invalidates: ['Connection[]'],
  },
  'connections.check': {
    method: 'POST',
    path: '/api/v1/connections/{id}/check',
    entity: 'Connection',
    rootType: 'Connection',
    provides: ['Connection:{id}'],
    invalidates: ['Connection[]'],
    responseCodec: CODECS['Connection'],
  },
  'connections.replace': {
    method: 'PUT',
    path: '/api/v1/connections/{id}/credential',
    entity: 'Connection',
    rootType: 'Connection',
    provides: ['Connection:{id}'],
    invalidates: ['Connection[]'],
    bodyCodec: CODECS['ReplaceInputBody'],
    responseCodec: CODECS['Connection'],
  },
  'deployments.get': {
    method: 'GET',
    path: '/api/v1/deployments/{id}',
    entity: 'Deployment',
    rootType: 'Deployment',
    provides: ['Deployment:{id}'],
    invalidates: [],
    responseCodec: CODECS['Deployment'],
  },
  'deployments.retry': {
    method: 'POST',
    path: '/api/v1/deployments/{id}/retry',
    entity: 'Deployment',
    rootType: 'Deployment',
    provides: ['Deployment:{id}'],
    invalidates: ['Deployment[]'],
    responseCodec: CODECS['Deployment'],
  },
  'github.status': {
    method: 'GET',
    path: '/api/v1/github',
    entity: 'GitHubInstallation',
    rootType: 'GitHubStatus',
    provides: ['GitHubInstallation:{id}', 'GitHubInstallation[]'],
    invalidates: [],
    responseCodec: CODECS['GitHubStatus'],
  },
  'github.connect': {
    method: 'POST',
    path: '/api/v1/github/installations',
    entity: 'GitHubInstallation',
    rootType: 'GitHubInstallation',
    provides: ['GitHubInstallation:{id}'],
    invalidates: ['GitHubInstallation[]'],
    bodyCodec: CODECS['ConnectGitHubInputBody'],
    responseCodec: CODECS['GitHubInstallation'],
  },
  'github.disconnect': {
    method: 'DELETE',
    path: '/api/v1/github/installations/{id}',
    entity: 'GitHubInstallation',
    provides: [],
    invalidates: ['GitHubInstallation[]'],
  },
  'github.repositories': {
    method: 'GET',
    path: '/api/v1/github/repositories',
    provides: [],
    invalidates: [],
    responseCodec: CODECS['ConnectedRepositoryList'],
  },
  'infrastructure.get': {
    method: 'GET',
    path: '/api/v1/infrastructure',
    provides: [],
    invalidates: [],
    responseCodec: CODECS['InfrastructureStatus'],
  },
  'invitations.list': {
    method: 'GET',
    path: '/api/v1/invitations',
    entity: 'OrgInvitation',
    rootType: 'OrgInvitationList',
    provides: ['OrgInvitation:{id}', 'OrgInvitation[]'],
    invalidates: [],
    responseCodec: CODECS['OrgInvitationList'],
  },
  'invitations.create': {
    method: 'POST',
    path: '/api/v1/invitations',
    provides: [],
    invalidates: ['OrgInvitation[]'],
    bodyCodec: CODECS['CreateInvitationInputBody'],
    responseCodec: CODECS['CreatedInvitation'],
  },
  'members.list': {
    method: 'GET',
    path: '/api/v1/members',
    entity: 'OrgMember',
    rootType: 'OrgMemberList',
    provides: ['OrgMember:{id}', 'OrgMember[]'],
    invalidates: [],
    responseCodec: CODECS['OrgMemberList'],
  },
  'plans.get': {
    method: 'GET',
    path: '/api/v1/plans/{id}',
    entity: 'Plan',
    rootType: 'Plan',
    provides: ['Plan:{id}'],
    invalidates: [],
    responseCodec: CODECS['Plan'],
  },
  'plans.approve': {
    method: 'POST',
    path: '/api/v1/plans/{id}/approve',
    entity: 'Plan',
    rootType: 'Plan',
    provides: ['Plan:{id}'],
    invalidates: ['Plan[]'],
    bodyCodec: CODECS['DecideInputBody'],
    responseCodec: CODECS['Plan'],
  },
  'deployments.forPlan': {
    method: 'GET',
    path: '/api/v1/plans/{id}/deployment',
    entity: 'Deployment',
    rootType: 'Deployment',
    provides: ['Deployment:{id}'],
    invalidates: [],
    responseCodec: CODECS['Deployment'],
  },
  'plans.reject': {
    method: 'POST',
    path: '/api/v1/plans/{id}/reject',
    entity: 'Plan',
    rootType: 'Plan',
    provides: ['Plan:{id}'],
    invalidates: ['Plan[]'],
    bodyCodec: CODECS['DecideInputBody'],
    responseCodec: CODECS['Plan'],
  },
  'projects.list': {
    method: 'GET',
    path: '/api/v1/projects',
    entity: 'Project',
    rootType: 'ProjectList',
    provides: ['Project:{id}', 'Project[]'],
    invalidates: [],
    responseCodec: CODECS['ProjectList'],
  },
  'projects.create': {
    method: 'POST',
    path: '/api/v1/projects',
    entity: 'Project',
    rootType: 'Project',
    provides: ['Project:{id}'],
    invalidates: ['Project[]'],
    bodyCodec: CODECS['CreateProjectInputBody'],
    responseCodec: CODECS['Project'],
  },
  'projects.get': {
    method: 'GET',
    path: '/api/v1/projects/{id}',
    entity: 'Project',
    rootType: 'Project',
    provides: ['Project:{id}'],
    invalidates: [],
    responseCodec: CODECS['Project'],
  },
  'projects.update': {
    method: 'PATCH',
    path: '/api/v1/projects/{id}',
    entity: 'Project',
    rootType: 'Project',
    provides: ['Project:{id}'],
    invalidates: ['Project[]'],
    bodyCodec: CODECS['UpdateProjectInputBody'],
    responseCodec: CODECS['Project'],
  },
  'projects.delete': {
    method: 'DELETE',
    path: '/api/v1/projects/{id}',
    entity: 'Project',
    provides: [],
    invalidates: ['Project[]'],
  },
  'analyses.list': {
    method: 'GET',
    path: '/api/v1/projects/{id}/analyses',
    entity: 'Analysis',
    rootType: 'AnalysisList',
    provides: ['Analysis:{id}', 'Analysis[]'],
    invalidates: [],
    responseCodec: CODECS['AnalysisList'],
  },
  'analyses.create': {
    method: 'POST',
    path: '/api/v1/projects/{id}/analyses',
    entity: 'Analysis',
    rootType: 'AnalysisList',
    provides: ['Analysis:{id}'],
    invalidates: ['Analysis[]'],
    bodyCodec: CODECS['StartAnalysisInputBody'],
    responseCodec: CODECS['AnalysisList'],
  },
  'bindings.list': {
    method: 'GET',
    path: '/api/v1/projects/{id}/bindings',
    entity: 'Binding',
    rootType: 'BindingList',
    provides: ['Binding:{id}', 'Binding[]'],
    invalidates: [],
    responseCodec: CODECS['BindingList'],
  },
  'deployments.list': {
    method: 'GET',
    path: '/api/v1/projects/{id}/deployments',
    entity: 'Deployment',
    rootType: 'DeploymentList',
    provides: ['Deployment:{id}', 'Deployment[]'],
    invalidates: [],
    responseCodec: CODECS['DeploymentList'],
  },
  'environments.list': {
    method: 'GET',
    path: '/api/v1/projects/{id}/environments',
    entity: 'Environment',
    rootType: 'EnvironmentList',
    provides: ['Environment:{id}', 'Environment[]'],
    invalidates: [],
    responseCodec: CODECS['EnvironmentList'],
  },
  'environments.create': {
    method: 'POST',
    path: '/api/v1/projects/{id}/environments',
    entity: 'Environment',
    rootType: 'Environment',
    provides: ['Environment:{id}'],
    invalidates: ['Environment[]'],
    bodyCodec: CODECS['CreateEnvironmentInputBody'],
    responseCodec: CODECS['Environment'],
  },
  'environments.delete': {
    method: 'DELETE',
    path: '/api/v1/projects/{id}/environments/{envId}',
    entity: 'Environment',
    provides: [],
    invalidates: ['Environment[]'],
  },
  'environmentConnections.get': {
    method: 'GET',
    path: '/api/v1/projects/{id}/environments/{environment}/connections',
    provides: [],
    invalidates: [],
    responseCodec: CODECS['EnvironmentConnections'],
  },
  'environmentConnections.choose': {
    method: 'PUT',
    path: '/api/v1/projects/{id}/environments/{environment}/connections/{cloud}',
    provides: [],
    invalidates: [],
    bodyCodec: CODECS['ChooseConnectionInputBody'],
    responseCodec: CODECS['EnvironmentConnections'],
  },
  'deployed.list': {
    method: 'GET',
    path: '/api/v1/projects/{id}/environments/{environment}/deployed',
    provides: [],
    invalidates: [],
    responseCodec: CODECS['DeployedList'],
  },
  'projects.graph': {
    method: 'GET',
    path: '/api/v1/projects/{id}/graph',
    entity: 'ProjectView',
    rootType: 'ProjectView',
    provides: ['ProjectView:{projectId}'],
    invalidates: [],
    responseCodec: CODECS['ProjectView'],
  },
  'manifests.current': {
    method: 'GET',
    path: '/api/v1/projects/{id}/manifest',
    entity: 'Revision',
    rootType: 'Revision',
    provides: ['Revision:{id}'],
    invalidates: [],
    responseCodec: CODECS['Revision'],
  },
  'manifests.save': {
    method: 'PUT',
    path: '/api/v1/projects/{id}/manifest',
    entity: 'Revision',
    rootType: 'Revision',
    provides: ['Revision:{id}'],
    invalidates: ['Revision[]'],
    bodyCodec: CODECS['SaveInputBody'],
    responseCodec: CODECS['Revision'],
  },
  'manifests.draft': {
    method: 'GET',
    path: '/api/v1/projects/{id}/manifest/draft',
    entity: 'ManifestDraft',
    rootType: 'ManifestDraft',
    provides: ['ManifestDraft:{projectId}'],
    invalidates: [],
    responseCodec: CODECS['ManifestDraft'],
  },
  'manifests.edit': {
    method: 'POST',
    path: '/api/v1/projects/{id}/manifest/edits',
    entity: 'Revision',
    rootType: 'Revision',
    provides: ['Revision:{id}'],
    invalidates: ['Revision[]'],
    bodyCodec: CODECS['EditInputBody'],
    responseCodec: CODECS['Revision'],
  },
  'manifests.history': {
    method: 'GET',
    path: '/api/v1/projects/{id}/manifest/revisions',
    entity: 'Revision',
    rootType: 'RevisionList',
    provides: ['Revision:{id}', 'Revision[]'],
    invalidates: [],
    responseCodec: CODECS['RevisionList'],
  },
  'plans.list': {
    method: 'GET',
    path: '/api/v1/projects/{id}/plans',
    entity: 'Plan',
    rootType: 'PlanList',
    provides: ['Plan:{id}', 'Plan[]'],
    invalidates: [],
    responseCodec: CODECS['PlanList'],
  },
  'plans.create': {
    method: 'POST',
    path: '/api/v1/projects/{id}/plans',
    entity: 'Plan',
    rootType: 'Plan',
    provides: ['Plan:{id}'],
    invalidates: ['Plan[]'],
    bodyCodec: CODECS['CreatePlanInputBody'],
    responseCodec: CODECS['Plan'],
  },
  'sources.list': {
    method: 'GET',
    path: '/api/v1/projects/{id}/sources',
    entity: 'Source',
    rootType: 'SourceList',
    provides: ['Source:{id}', 'Source[]'],
    invalidates: [],
    responseCodec: CODECS['SourceList'],
  },
  'sources.create': {
    method: 'POST',
    path: '/api/v1/projects/{id}/sources',
    entity: 'Source',
    rootType: 'Source',
    provides: ['Source:{id}'],
    invalidates: ['Source[]'],
    bodyCodec: CODECS['AddSourceInputBody'],
    responseCodec: CODECS['Source'],
  },
  'sources.update': {
    method: 'PATCH',
    path: '/api/v1/projects/{id}/sources/{alias}',
    entity: 'Source',
    rootType: 'Source',
    provides: ['Source:{id}'],
    invalidates: ['Source[]'],
    bodyCodec: CODECS['UpdateSourceInputBody'],
    responseCodec: CODECS['Source'],
  },
  'sources.delete': {
    method: 'DELETE',
    path: '/api/v1/projects/{id}/sources/{alias}',
    entity: 'Source',
    provides: [],
    invalidates: ['Source[]'],
  },
  'projectVariables.list': {
    method: 'GET',
    path: '/api/v1/projects/{id}/variables',
    entity: 'Variable',
    rootType: 'VariableList',
    provides: ['Variable:{id}', 'Variable[]'],
    invalidates: [],
    responseCodec: CODECS['VariableList'],
  },
  'projectVariables.create': {
    method: 'POST',
    path: '/api/v1/projects/{id}/variables',
    entity: 'Variable',
    rootType: 'Variable',
    provides: ['Variable:{id}'],
    invalidates: ['Variable[]'],
    bodyCodec: CODECS['CreateVariableInputBody'],
    responseCodec: CODECS['Variable'],
  },
  'variables.resolve': {
    method: 'GET',
    path: '/api/v1/projects/{id}/variables/resolved',
    rootType: 'Resolution',
    provides: [],
    invalidates: [],
    responseCodec: CODECS['Resolution'],
  },
  'providers.list': {
    method: 'GET',
    path: '/api/v1/providers',
    provides: [],
    invalidates: [],
    responseCodec: CODECS['ProviderList'],
  },
  'publicConfig.get': {
    method: 'GET',
    path: '/api/v1/public-config',
    provides: [],
    invalidates: [],
    responseCodec: CODECS['PublicConfig'],
  },
  'session.get': {
    method: 'GET',
    path: '/api/v1/session',
    provides: [],
    invalidates: [],
    responseCodec: CODECS['Session'],
  },
  'variables.list': {
    method: 'GET',
    path: '/api/v1/variables',
    entity: 'Variable',
    rootType: 'VariableList',
    provides: ['Variable:{id}', 'Variable[]'],
    invalidates: [],
    responseCodec: CODECS['VariableList'],
  },
  'variables.create': {
    method: 'POST',
    path: '/api/v1/variables',
    entity: 'Variable',
    rootType: 'Variable',
    provides: ['Variable:{id}'],
    invalidates: ['Variable[]'],
    bodyCodec: CODECS['CreateVariableInputBody'],
    responseCodec: CODECS['Variable'],
  },
  'variables.update': {
    method: 'PATCH',
    path: '/api/v1/variables/{id}',
    entity: 'Variable',
    rootType: 'Variable',
    provides: ['Variable:{id}'],
    invalidates: ['Variable[]'],
    bodyCodec: CODECS['UpdateVariableInputBody'],
    responseCodec: CODECS['Variable'],
  },
  'variables.delete': {
    method: 'DELETE',
    path: '/api/v1/variables/{id}',
    entity: 'Variable',
    provides: [],
    invalidates: ['Variable[]'],
  },
} as const satisfies Record<string, OperationMeta>;

export const entities = {
  'APIKey': { idField: 'id' },
  'APIKeyList': { fields: { 'keys': 'APIKey' } },
  'Analysis': { idField: 'id' },
  'AnalysisGraph': { idField: 'id' },
  'AnalysisList': { fields: { 'analyses': 'Analysis' } },
  'Binding': { idField: 'id' },
  'BindingList': { fields: { 'bindings': 'Binding' } },
  'Connection': { idField: 'id' },
  'ConnectionList': { fields: { 'connections': 'Connection' } },
  'CreatedKey': { fields: { 'key': 'APIKey' } },
  'Deployment': { idField: 'id' },
  'DeploymentList': { fields: { 'deployments': 'Deployment' } },
  'Environment': { idField: 'id' },
  'EnvironmentList': { fields: { 'environments': 'Environment' } },
  'GitHubInstallation': { idField: 'id' },
  'GitHubStatus': { fields: { 'installations': 'GitHubInstallation' } },
  'ManifestDraft': { idField: 'projectId' },
  'OrgInvitation': { idField: 'id' },
  'OrgInvitationList': { fields: { 'invitations': 'OrgInvitation' } },
  'OrgMember': { idField: 'id' },
  'OrgMemberList': { fields: { 'members': 'OrgMember' } },
  'Plan': { idField: 'id' },
  'PlanList': { fields: { 'plans': 'Plan' } },
  'Project': { idField: 'id' },
  'ProjectList': { fields: { 'projects': 'Project' } },
  'ProjectView': { idField: 'projectId', fields: { 'sources': 'SourceState' } },
  'Resolution': { fields: { 'variables': 'ResolvedVariable' } },
  'ResolvedVariable': { fields: { 'overrides': 'Variable', 'variable': 'Variable' } },
  'Revision': { idField: 'id' },
  'RevisionList': { fields: { 'revisions': 'Revision' } },
  'Source': { idField: 'id' },
  'SourceList': { fields: { 'sources': 'Source' } },
  'SourceState': { fields: { 'latest': 'Analysis', 'shown': 'Analysis', 'source': 'Source' } },
  'Variable': { idField: 'id' },
  'VariableList': { fields: { 'variables': 'Variable' } },
} as const satisfies Record<string, EntityMeta>;

export const streams = [
] as const;
