// Generated types

export enum ConnectionState {
  DISCONNECTED = 'disconnected',
  CONNECTING = 'connecting',
  CONNECTED = 'connected',
  RECONNECTING = 'reconnecting',
  CLOSED = 'closed',
  ERROR = 'error',
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
  summary: Summary;
}

export interface AnalysisGraph {
  graph: Graph;
  id: string;
}

export interface AnalysisList {
  analyses: Analysis[];
}

export interface BuildSystem {
  evidence: Evidence;
  name: string;
  path: string;
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

export interface CreateInvitationInputBody {
  email: string;
  role?: string;
}

export interface CreateProjectInputBody {
  description?: string;
  name: string;
  repositoryUrl?: string;
  slug?: string;
}

export interface CreatedInvitation {
  acceptPath: string;
  invitation: OrgInvitation;
}

export interface Edge {
  confidence: string;
  evidence: Evidence[];
  from: string;
  kind: string;
  to: string;
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

export interface EnvironmentList {
  environments: Environment[];
}

export interface Evidence {
  detail: string;
  line?: number;
  path: string;
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

export interface InfrastructureStatus {
  engine: string;
  providers: ProviderStatus[];
  store: string;
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

export interface Project {
  createdAt: string;
  createdBy: string;
  description: string;
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

export interface ProviderStatus {
  capabilities: string[];
  name: string;
  region: string;
  version: string;
}

export interface PublicConfig {
  appId: string;
  authBasePath: string;
  publishableKey: string;
}

export interface Resource {
  confidence: string;
  evidence: Evidence[];
  id: string;
  kind: string;
  needsConfirmation: boolean;
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

export interface Session {
  orgId: string;
  permissions: string[];
  userId: string;
}

export interface StartAnalysisInputBody {
  ref?: string;
}

export interface Summary {
  deployable: number;
  needsConfirmation: number;
  packages: number;
  resources: number;
  services: number;
}

export interface UpdateProjectInputBody {
  description?: string;
  name?: string;
  repositoryUrl?: string;
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

