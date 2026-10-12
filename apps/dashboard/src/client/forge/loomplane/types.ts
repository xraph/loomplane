// Generated types

export enum ConnectionState {
  DISCONNECTED = 'disconnected',
  CONNECTING = 'connecting',
  CONNECTED = 'connected',
  RECONNECTING = 'reconnecting',
  CLOSED = 'closed',
  ERROR = 'error',
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

export interface InfrastructureStatus {
  engine: string;
  providers: ProviderStatus[];
  store: string;
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

export interface Session {
  orgId: string;
  permissions: string[];
  userId: string;
}

export interface UpdateProjectInputBody {
  description?: string;
  name?: string;
  repositoryUrl?: string;
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

