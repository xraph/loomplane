// Generated types

export enum ConnectionState {
  DISCONNECTED = 'disconnected',
  CONNECTING = 'connecting',
  CONNECTED = 'connected',
  RECONNECTING = 'reconnecting',
  CLOSED = 'closed',
  ERROR = 'error',
}

export interface AcceptInvitationRequest {
  /** Invitation token */
  token: string;
}

export interface AddMemberRequest {
  /** Member role (owner, admin, member) */
  role?: string;
  /** User identifier to add */
  userId: string;
}

export interface CreateInvitationRequest {
  /** Email address to invite */
  email: string;
  /** Member role (owner, admin, member) */
  role?: string;
}

export interface CreateOrgRequest {
  /** Application ID (optional, uses default) */
  appId?: string;
  /** Organization logo URL */
  logo?: string;
  /** Organization name */
  name: string;
  /** URL-safe slug */
  slug: string;
}

export interface DeclineInvitationRequest {
  /** Invitation token */
  token: string;
}

export interface Invitation {
  createdAt: string;
  email: string;
  expiresAt: string;
  id: string;
  inviterId: string;
  orgId: string;
  role: string;
  status: string;
}

export interface InvitationListResponse {
  invitations: Invitation[];
  /** Cursor for the next page, absent on the last */
  nextCursor?: string;
}

export interface Member {
  createdAt: string;
  id: string;
  orgId: string;
  role: string;
  updatedAt: string;
  userId: string;
}

export interface MemberListResponse {
  members: Member[];
  /** Cursor for the next page, absent on the last */
  nextCursor?: string;
}

export interface OrgListResponse {
  /** Cursor for the next page, absent on the last */
  nextCursor?: string;
  organizations: Organization[];
}

export interface Organization {
  appId: string;
  createdAt: string;
  createdBy: string;
  envId: string;
  id: string;
  logo?: string;
  metadata?: Record<string, any>;
  name: string;
  slug: string;
  updatedAt: string;
}

export interface OrganizationStatusResponse {
  /** Operation status */
  status: string;
}

export interface SlugAvailableResponse {
  /** Whether the slug is available */
  available: boolean;
}

export interface SwitchOrgRequest {
  /** Organization ID to activate; empty to clear */
  orgId: string;
}

export interface SwitchOrgResponse {
  orgId?: string;
  sessionId: string;
}

export interface UpdateMemberRequest {
  /** New member role (owner, admin, member) */
  role: string;
}

export interface UpdateOrgRequest {
  /** Organization logo URL */
  logo?: string;
  /** Organization name */
  name?: string;
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

