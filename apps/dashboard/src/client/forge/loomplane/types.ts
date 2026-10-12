// Generated types

export enum ConnectionState {
  DISCONNECTED = 'disconnected',
  CONNECTING = 'connecting',
  CONNECTED = 'connected',
  RECONNECTING = 'reconnecting',
  CLOSED = 'closed',
  ERROR = 'error',
}

export interface Action {
  changes?: Change[];
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
  startedAt?: string;
  status: string;
  summary: GraphSummary;
}

export interface AnalysisGraph {
  graph: Graph;
  id: string;
}

export interface AnalysisList {
  analyses: Analysis[];
}

export interface Binding {
  config: string;
  createdAt: string;
  engine: string;
  environmentId: string;
  externalId: string;
  id: string;
  name: string;
  orgId: string;
  planId: string;
  projectId: string;
  provider: string;
  region: string;
  subject: string;
  updatedAt: string;
}

export interface BindingList {
  bindings: Binding[];
}

export interface BuildSystem {
  evidence: Evidence;
  name: string;
  path: string;
}

export interface Change {
  field: string;
  from: string;
  to: string;
}

export interface ChooseConnectionInputBody {
  connectionId: string;
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
  environment: string;
}

export interface CreateProjectInputBody {
  color?: string;
  description?: string;
  icon?: string;
  name: string;
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

export interface DecideInputBody {
  note?: string;
  revision?: string;
}

export interface Defaults {
  provider?: string;
  region?: string;
}

export interface Edge {
  confidence: string;
  evidence: Evidence[];
  from: string;
  kind: string;
  to: string;
}

export interface EnvLink {
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
  createdAt: string;
  id: string;
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

export interface Ignore {
  resources?: string[];
}

export interface InfrastructureStatus {
  engine: string;
  providers: ProviderStatus[];
  store: string;
}

export interface Manifest {
  defaults: Defaults;
  ignore: Ignore;
  resources?: Record<string, any>;
  services?: Record<string, any>;
  version: number;
}

export interface ManifestDraft {
  analysisId: string;
  commitSha: string;
  manifest?: Manifest;
  projectId: string;
  yaml: string;
}

export interface Note {
  message: string;
  path?: string;
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
  runtime?: string;
}

export interface Plan {
  actions: Action[];
  analysisId: string;
  commitSha: string;
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
  status: string;
  summary: PlanningSummary;
  warnings: Finding[];
}

export interface PlanList {
  plans: Plan[];
}

export interface PlanningSummary {
  create: number;
  delete: number;
  keep: number;
  replace: number;
  update: number;
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
  repositoryUrl: string;
  slug: string;
  status: string;
  updatedAt: string;
}

export interface ProjectList {
  projects: Project[];
}

export interface Provider {
  available: boolean;
  cloud: string;
  description: string;
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

export interface ReplaceInputBody {
  credential: string;
  settings?: Record<string, any>;
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
  kind: string;
  provider: string;
  region?: string;
  version?: string;
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

export interface Runs {
  creates: string;
  execution: string;
  workload: string;
}

export interface SaveInputBody {
  manifest?: {
  defaults: Defaults;
  ignore: Ignore;
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
  runtime?: string;
  unit?: string;
}

export interface ServiceConfig {
  build?: string;
  ecosystem: string;
  env?: EnvLink[];
  execution: string;
  framework?: string;
  image?: string;
  port?: number;
  provider: string;
  public: boolean;
  region?: string;
  scale?: Scale;
  size?: Size;
  source: string;
  start?: string;
  workload: string;
}

export interface Session {
  orgId: string;
  permissions: string[];
  userId: string;
}

export interface Size {
  cpu: number;
  memory: number;
}

export interface StartAnalysisInputBody {
  ref?: string;
}

export interface UpdateProjectInputBody {
  color?: string;
  description?: string;
  icon?: string;
  name?: string;
  repositoryUrl?: string;
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

