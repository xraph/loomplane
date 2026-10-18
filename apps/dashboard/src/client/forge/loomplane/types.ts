// Generated types

export enum ConnectionState {
  DISCONNECTED = 'disconnected',
  CONNECTING = 'connecting',
  CONNECTED = 'connected',
  RECONNECTING = 'reconnecting',
  CLOSED = 'closed',
  ERROR = 'error',
}

export interface APIKey {
  access: string;
  createdAt: string;
  createdBy: string;
  expiresAt?: string;
  hint: string;
  id: string;
  lastUsedAt?: string;
  name: string;
  revokedAt?: string;
  state: string;
}

export interface APIKeyList {
  keys: APIKey[];
}

export interface Action {
  changes?: Change[];
  connectionId?: string;
  creates: string;
  dependsOn?: string[];
  engine: string;
  id: string;
  name: string;
  op: string;
  provider: string;
  reason: string;
  region?: string;
  resource?: ResourceConfig;
  service?: ServiceConfig;
  step: number;
  subject: string;
}

export interface ActivityDay {
  date: string;
  failed: number;
  succeeded: number;
}

export interface ActivitySummary {
  analyses: AnalysisCounts;
  daily: ActivityDay[];
  days: number;
  deployments: DeploymentCounts;
  drift: DriftCounts;
  plans: PlanCounts;
  since: string;
}

export interface AddSourceInputBody {
  alias?: string;
  ref?: string;
  repository: string;
  root?: string;
}

export interface Analysis {
  commitSha: string;
  createdAt: string;
  createdBy: string;
  error: string;
  finishedAt?: string;
  id: string;
  orgId: string;
  projectId: string;
  ref: string;
  repository: string;
  source: string;
  sourceId: string;
  startedAt?: string;
  status: string;
  summary: GraphSummary;
}

export interface AnalysisCounts {
  failed: number;
  succeeded: number;
}

export interface AnalysisGraph {
  graph: Graph;
  id: string;
}

export interface AnalysisList {
  analyses: Analysis[];
}

export interface Backup {
  attempt: number;
  bytes: number;
  completedAt?: string;
  createdAt: string;
  createdBy: string;
  environmentId: string;
  error?: string;
  id: string;
  name: string;
  orgId: string;
  projectId: string;
  restoredAt?: string;
  sha256?: string;
  status: string;
}

export interface BackupCreateInputBody {
  environment: string;
  requestId: string;
  resource: string;
}

export interface BackupList {
  backups: Backup[];
}

export interface Binding {
  commitSha: string;
  config: string;
  connectionId: string;
  createdAt: string;
  drift: string;
  driftMessage: string;
  engine: string;
  environmentId: string;
  externalId: string;
  id: string;
  name: string;
  observedAt?: string;
  orgId: string;
  planId: string;
  projectId: string;
  provider: string;
  region: string;
  revision: string;
  subject: string;
  updatedAt: string;
  url: string;
}

export interface BindingList {
  bindings: Binding[];
}

export interface Build {
  builder?: string;
  context?: string;
  image?: string;
  registry?: string;
}

export interface BuildSystem {
  evidence: Evidence;
  name: string;
  path: string;
  source?: string;
}

export interface BuildTarget {
  builder?: string;
  builderSettings?: Record<string, any>;
  image?: string;
  registry?: string;
  registrySettings?: Record<string, any>;
}

export interface Change {
  field: string;
  from: string;
  to: string;
}

export interface ChooseConnectionInputBody {
  connectionId: string;
}

export interface CloudForm {
  builtin: boolean;
  cloud: string;
  credential?: string;
  help?: string;
  settings?: Setting[];
  title: string;
}

export interface CloudList {
  clouds: CloudForm[];
}

export interface Collection {
  actionId: string;
  attempt: string;
  deploymentId: string;
  id: string;
  message: string;
  operationId: string;
  orgId: string;
  projectId: string;
  state: string;
  updatedAt: string;
}

export interface Commands {
  build?: string;
  dev?: string;
  start?: string;
}

export interface ConnectGitHubInputBody {
  code: string;
  installationId: number;
  state: string;
}

export interface ConnectedRepository {
  defaultBranch: string;
  fullName: string;
  installation: string;
  private: boolean;
  url: string;
}

export interface ConnectedRepositoryList {
  repositories: ConnectedRepository[];
}

export interface Connection {
  account: string;
  checkedAt?: string;
  cloud: string;
  createdAt: string;
  createdBy: string;
  details: Record<string, any>;
  id: string;
  message: string;
  missing: string[];
  name: string;
  orgId: string;
  status: string;
  updatedAt: string;
}

export interface ConnectionList {
  connections: Connection[];
}

export interface CreateEnvironmentInputBody {
  kind?: string;
  name: string;
}

export interface CreateInputBody {
  cloud: string;
  credential: string;
  name: string;
  settings?: Record<string, any>;
}

export interface CreateInvitationInputBody {
  email: string;
  role?: string;
}

export interface CreatePlanInputBody {
  catalog?: boolean;
  environment: string;
  scope?: string[];
}

export interface CreateProjectInputBody {
  color?: string;
  description?: string;
  icon?: string;
  name: string;
  repositories?: {
  alias?: string;
  ref?: string;
  repository: string;
  root?: string;
}[];
  repositoryUrl?: string;
  slug?: string;
}

export interface CreateVariableInputBody {
  environment?: string;
  key: string;
  secret?: boolean;
  service?: string;
  value: string;
}

export interface CreatedInvitation {
  acceptPath: string;
  invitation: OrgInvitation;
}

export interface CreatedKey {
  key: APIKey;
  secret: string;
}

export interface DecideInputBody {
  note?: string;
  revision?: string;
}

export interface Defaults {
  provider?: string;
  region?: string;
}

export interface DeployedList {
  sources: DeployedRepository[];
}

export interface DeployedRepository {
  alias: string;
  commitSha: string;
  deployedAt: string;
  planId: string;
  repository: string;
  sourceId: string;
}

export interface Deployment {
  commitSha: string;
  environment: string;
  environmentId: string;
  error: string;
  finishedAt?: string;
  id: string;
  orgId: string;
  planId: string;
  projectId: string;
  retries: number;
  sources: SourceArtifact[];
  startedAt: string;
  startedBy: string;
  status: string;
  steps: Step[];
}

export interface DeploymentCounts {
  failed: number;
  medianSeconds: number;
  retried: number;
  running: number;
  succeeded: number;
}

export interface DeploymentList {
  deployments: Deployment[];
  next?: string;
}

export interface DriftCounts {
  changed: number;
  deployed: number;
  missing: number;
}

export interface Edge {
  confidence: string;
  evidence: Evidence[];
  from: string;
  kind: string;
  to: string;
}

export interface EditInputBody {
  addResource?: {
  kind: string;
  name: string;
  provider?: string;
};
  ignore?: string;
  placement?: {
  build?: Build;
  enabled?: boolean;
  endpoint?: Endpoint;
  environment?: string;
  healthPath?: string;
  name: string;
  provider?: string;
  region?: string;
  section: string;
  target?: string;
};
}

export interface Endpoint {
  hostPort?: number;
  hostname?: string;
  ingressClass?: string;
  tlsSecret?: string;
}

export interface EnvLink {
  address?: string;
  from: string;
  key: string;
  subject: string;
}

export interface Environment {
  createdAt: string;
  id: string;
  kind: string;
  name: string;
  orgId: string;
  projectId: string;
  updatedAt: string;
}

export interface EnvironmentConnections {
  connections: Record<string, any>;
  environment: string;
}

export interface EnvironmentList {
  environments: Environment[];
}

export interface Evidence {
  detail: string;
  line?: number;
  path: string;
  source?: string;
}

export interface Federation {
  accessGrants?: Record<string, any>;
  destinations?: Record<string, any>;
  networkLinks?: Record<string, any>;
  placements?: Record<string, any>;
  serviceExports?: Record<string, any>;
}

export interface Finding {
  code: string;
  evidence?: Evidence[];
  message: string;
  name?: string;
  path?: string;
  subject?: string;
}

export interface GitHubInstallation {
  account: string;
  accountType: string;
  avatarUrl: string;
  createdAt: string;
  id: string;
  manageUrl: string;
}

export interface GitHubStatus {
  configured: boolean;
  installUrl?: string;
  installations: GitHubInstallation[];
}

export interface Graph {
  buildSystems: BuildSystem[];
  edges: Edge[];
  notes: Note[];
  packages: Package[];
  resources: Resource[];
  services: Service[];
  sources?: SourceRef[];
  version: number;
  workspaces: Workspace[];
}

export interface GraphSummary {
  deployable: number;
  needsConfirmation: number;
  packages: number;
  resources: number;
  services: number;
}

export interface Held {
  sameAddress?: string;
}

export interface Ignore {
  resources?: string[];
}

export interface InfrastructureStatus {
  engine: string;
  providers: ProviderStatus[];
  store: string;
}

export interface Log {
  actionId: string;
  attempt: string;
  deploymentId: string;
  id: string;
  level: string;
  message: string;
  operationId: string;
  orgId: string;
  projectId: string;
  providerId: string;
  source: string;
  stream: string;
  time: string;
  truncated: boolean;
}

export interface LogPage {
  gap: boolean;
  logs: Log[];
  next: string;
}

export interface Manifest {
  defaults?: Defaults;
  federation?: Federation;
  gateways?: Record<string, any>;
  held?: Held;
  ignore?: Ignore;
  links?: Record<string, any>;
  resources?: Record<string, any>;
  services?: Record<string, any>;
  version: number;
}

export interface ManifestDraft {
  manifest?: Manifest;
  projectId: string;
  sources: SourceRef[];
  yaml: string;
}

export interface NewKeyBody {
  access: string;
  expiresInDays?: number;
  name: string;
}

export interface Note {
  message: string;
  path?: string;
  source?: string;
}

export interface OrgInvitation {
  createdAt: string;
  email: string;
  expiresAt: string;
  id: string;
  invitedBy: string;
  role: string;
}

export interface OrgInvitationList {
  invitations: OrgInvitation[];
}

export interface OrgMember {
  email: string;
  id: string;
  joinedAt: string;
  memberRole: string;
  name: string;
  role: string;
  userId: string;
}

export interface OrgMemberList {
  members: OrgMember[];
}

export interface Package {
  ecosystem: string;
  evidence: Evidence[];
  id: string;
  name: string;
  path: string;
  requires?: Requirement[];
  runtime?: string;
  source?: string;
}

export interface Plan {
  actions: Action[];
  catalog: boolean;
  connections: Record<string, any>;
  createdAt: string;
  createdBy: string;
  decidedAt?: string;
  decidedBy: string;
  decisionNote: string;
  environment: string;
  environmentId: string;
  error: string;
  executable: boolean;
  expiresAt?: string;
  id: string;
  manifestId: string;
  manifestRevision: number;
  orgId: string;
  plannedAt?: string;
  problems: Finding[];
  projectId: string;
  revision: string;
  sources: PlanSource[];
  status: string;
  summary: PlanningSummary;
  warnings: Finding[];
}

export interface PlanCounts {
  approved: number;
  expired: number;
  failed: number;
  rejected: number;
  superseded: number;
  waiting: number;
}

export interface PlanList {
  next?: string;
  plans: Plan[];
}

export interface PlanSource {
  alias: string;
  analysisId: string;
  commitSha: string;
  inScope: boolean;
  repository: string;
  root: string;
  sourceId: string;
}

export interface PlanningSummary {
  create: number;
  delete: number;
  keep: number;
  replace: number;
  update: number;
}

export interface Policy {
  createdBy: string;
  enabled: boolean;
  environment: string;
  environmentId: string;
  everyHours: number;
  id: string;
  lastError?: string;
  nextAt: string;
  orgId: string;
  projectId: string;
  resource: string;
}

export interface PolicyInputBody {
  enabled: boolean;
  environment: string;
  everyHours: number;
  resource: string;
}

export interface PolicyList {
  policies: Policy[];
}

export interface Project {
  color: string;
  createdAt: string;
  createdBy: string;
  description: string;
  icon: string;
  id: string;
  name: string;
  orgId: string;
  repositories: string[];
  slug: string;
  status: string;
  updatedAt: string;
}

export interface ProjectList {
  projects: Project[];
}

export interface ProjectView {
  graph?: Graph;
  projectId: string;
  sources: SourceState[];
}

export interface Provider {
  available: boolean;
  buildLogs?: string;
  cloud: string;
  description: string;
  logNote?: string;
  model: string;
  name: string;
  note?: string;
  provisions?: Provisions[];
  regions?: string[];
  runs?: Runs[];
  scaling: boolean;
  title: string;
}

export interface ProviderList {
  providers: Provider[];
}

export interface ProviderStatus {
  capabilities: string[];
  name: string;
  region: string;
  version: string;
}

export interface Provisions {
  creates: string;
  kind: string;
  versions?: string[];
}

export interface PublicConfig {
  appId: string;
  authBasePath: string;
  publishableKey: string;
}

export interface RealtimeForgeGap {
  reason: string;
}

export interface RealtimeForgeResumed {
  count: number;
  from: string;
}

export interface RealtimeRealtimeSnapshot {
  cursor: string;
  partial: boolean;
  records: SnapshotRecord[];
}

export interface RealtimeRealtimeStatus {
  cursor: string;
  time: string;
}

export interface RealtimeReceive {
  channel: string;
  cursor: string;
  data: any;
  event: string;
}

export interface RealtimeSend {
  cursor?: string;
  deploymentIds?: string[];
  op: string;
  orgId: string;
  projectIds?: string[];
}

export interface Recipe {
  architecture: string;
  backupFormat: string;
  id: string;
  image: string;
  mountPath: string;
  productVersion: string;
  revision: string;
}

export interface ReplaceInputBody {
  credential: string;
  settings?: Record<string, any>;
}

export interface Requirement {
  evidence: Evidence;
  git?: string;
  name: string;
  version?: string;
}

export interface Resolution {
  environment: string;
  projectId: string;
  service?: string;
  variables: ResolvedVariable[];
}

export interface ResolvedVariable {
  key: string;
  overrides: Variable[];
  variable: Variable;
}

export interface Resource {
  confidence: string;
  evidence: Evidence[];
  id: string;
  kind: string;
  needsConfirmation: boolean;
}

export interface ResourceConfig {
  catalog?: Recipe;
  kind: string;
  provider: string;
  region?: string;
  target?: string;
  targetSettings?: Record<string, any>;
  version?: string;
}

export interface Restore {
  backupId: string;
  createdAt: string;
  createdBy: string;
  durationMs: number;
  error?: string;
  expiresAt: string;
  externalId?: string;
  id: string;
  orgId: string;
  projectId: string;
  status: string;
  verifiedAt?: string;
}

export interface RestoreInputBody {
  requestId: string;
}

export interface RestoreList {
  restores: Restore[];
}

export interface Revision {
  createdAt: string;
  createdBy: string;
  digest: string;
  id: string;
  manifest?: Manifest;
  orgId: string;
  projectId: string;
  revision: number;
  yaml: string;
}

export interface RevisionList {
  revisions: Revision[];
}

export interface Route {
  env: string;
  path: string;
  stripPrefix?: boolean;
  to: string;
}

export interface Runs {
  creates: string;
  execution: string;
  workload: string;
}

export interface SaveInputBody {
  baseRevision?: number;
  manifest?: {
  defaults?: Defaults;
  federation?: Federation;
  gateways?: Record<string, any>;
  held?: Held;
  ignore?: Ignore;
  links?: Record<string, any>;
  resources?: Record<string, any>;
  services?: Record<string, any>;
  version: number;
};
  yaml?: string;
}

export interface Scale {
  max: number;
  min: number;
}

export interface Service {
  commands: Commands;
  confidence: string;
  context?: string;
  deployable: boolean;
  ecosystem: string;
  entrypoint: string;
  evidence: Evidence[];
  framework?: string;
  id: string;
  image?: string;
  kind: string;
  name: string;
  path: string;
  ports?: number[];
  requires?: Requirement[];
  runtime?: string;
  source?: string;
  unit?: string;
  unitName?: string;
}

export interface ServiceConfig {
  build?: string;
  buildTarget?: BuildTarget;
  catalog?: Recipe;
  commitSha?: string;
  context?: string;
  ecosystem: string;
  endpoint?: Endpoint;
  env?: EnvLink[];
  execution: string;
  framework?: string;
  gateway?: Route[];
  healthPath?: string;
  image?: string;
  internal?: boolean;
  port?: number;
  privateEgress?: boolean;
  provider: string;
  public: boolean;
  region?: string;
  repo?: string;
  scale?: Scale;
  service: string;
  size?: Size;
  source: string;
  start?: string;
  target?: string;
  targetSettings?: Record<string, any>;
  workload: string;
}

export interface Session {
  orgId: string;
  permissions: string[];
  userId: string;
}

export interface Setting {
  key: string;
  label: string;
  required?: boolean;
}

export interface Size {
  cpu: number;
  memory: number;
}

export interface Snapshot {
  cursor: string;
  partial: boolean;
  records: SnapshotRecord[];
}

export interface SnapshotRecord {
  data: any;
  entityId: string;
  event: string;
  id: string;
  orgId: string;
  projectId?: string;
  time: string;
}

export interface Source {
  alias: string;
  createdAt: string;
  createdBy: string;
  id: string;
  orgId: string;
  projectId: string;
  ref: string;
  removedAt?: string;
  repository: string;
  root: string;
  updatedAt: string;
}

export interface SourceArtifact {
  alias: string;
  commitSha: string;
  key: string;
  repository: string;
}

export interface SourceList {
  sources: Source[];
}

export interface SourceRef {
  alias: string;
  analysisId: string;
  commitSha: string;
  repository: string;
  root?: string;
}

export interface SourceState {
  latest?: Analysis;
  shown?: Analysis;
  source: Source;
}

export interface StartAnalysisInputBody {
  ref?: string;
  sources?: string[];
}

export interface Step {
  actionId: string;
  finishedAt?: string;
  image: string;
  message: string;
  name: string;
  op: string;
  order: number;
  provider: string;
  startedAt?: string;
  status: string;
  subject: string;
  url: string;
  warnings?: string[];
}

export interface UpdateProjectInputBody {
  color?: string;
  description?: string;
  icon?: string;
  name?: string;
}

export interface UpdateSourceInputBody {
  ref?: string;
  root?: string;
}

export interface UpdateVariableInputBody {
  secret?: boolean;
  value: string;
}

export interface Variable {
  createdAt: string;
  createdBy: string;
  environment: string;
  id: string;
  key: string;
  orgId: string;
  projectId?: string;
  scope: string;
  secret: boolean;
  service?: string;
  updatedAt: string;
  updatedBy: string;
  value: string;
}

export interface VariableList {
  variables: Variable[];
}

export interface Workspace {
  ecosystem: string;
  evidence: Evidence[];
  members: string[];
  path: string;
  source?: string;
  tool: string;
}

export interface AuthConfig {
  bearerToken?: string;
  apiKey?: string;
  customHeaders?: Record<string, string>;
}

export interface ClientConfig {
  baseURL: string;
  auth?: AuthConfig;
  timeout?: number;
}

