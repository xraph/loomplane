// Generated codec table
//
// Describes, per schema, how a wire payload maps onto its TypeScript
// shape. `ts` is the client-side field name derived from the wire name by
// the configured FieldNaming strategy (or a FieldOverrides entry); encode
// and decode below walk this table to rename between the two.
//
// Each entry is also emitted as its own module under src/codecs/, for a
// consumer that wants one codec rather than all of them.

import type { Codec, CodecRef } from './codec-runtime';
import { decode as decodeRef, encode as encodeRef } from './codec-runtime';

export type { Codec, CodecRef } from './codec-runtime';

export const CODECS: Record<string, Codec> = {
  "CreateInvitationInputBody": {"kind": "object", "fields": {"email": {"ts": "email"}, "role": {"ts": "role"}}, "required": ["email"]},
  "CreateProjectInputBody": {"kind": "object", "fields": {"description": {"ts": "description"}, "name": {"ts": "name"}, "repository_url": {"ts": "repositoryUrl"}, "slug": {"ts": "slug"}}, "required": ["name"]},
  "CreatedInvitation": {"kind": "object", "fields": {"accept_path": {"ts": "acceptPath"}, "invitation": {"ts": "invitation", "codec": () => CODECS["OrgInvitation"]}}, "required": ["accept_path", "invitation"]},
  "Environment": {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "id": {"ts": "id"}, "kind": {"ts": "kind"}, "name": {"ts": "name"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "updated_at": {"ts": "updatedAt"}}, "required": ["created_at", "id", "kind", "name", "org_id", "project_id", "updated_at"]},
  "EnvironmentList": {"kind": "object", "fields": {"environments": {"ts": "environments", "codec": () => CODECS["EnvironmentList.environments"]}}, "required": ["environments"]},
  "EnvironmentList.environments": {"kind": "array", "items": () => CODECS["Environment"]},
  "InfrastructureStatus": {"kind": "object", "fields": {"engine": {"ts": "engine"}, "providers": {"ts": "providers", "codec": () => CODECS["InfrastructureStatus.providers"]}, "store": {"ts": "store"}}, "required": ["engine", "providers", "store"]},
  "InfrastructureStatus.providers": {"kind": "array", "items": () => CODECS["ProviderStatus"]},
  "OrgInvitation": {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "email": {"ts": "email"}, "expires_at": {"ts": "expiresAt"}, "id": {"ts": "id"}, "invited_by": {"ts": "invitedBy"}, "role": {"ts": "role"}}, "required": ["created_at", "email", "expires_at", "id", "invited_by", "role"]},
  "OrgInvitationList": {"kind": "object", "fields": {"invitations": {"ts": "invitations", "codec": () => CODECS["OrgInvitationList.invitations"]}}, "required": ["invitations"]},
  "OrgInvitationList.invitations": {"kind": "array", "items": () => CODECS["OrgInvitation"]},
  "OrgMember": {"kind": "object", "fields": {"email": {"ts": "email"}, "id": {"ts": "id"}, "joined_at": {"ts": "joinedAt"}, "member_role": {"ts": "memberRole"}, "name": {"ts": "name"}, "role": {"ts": "role"}, "user_id": {"ts": "userId"}}, "required": ["email", "id", "joined_at", "member_role", "name", "role", "user_id"]},
  "OrgMemberList": {"kind": "object", "fields": {"members": {"ts": "members", "codec": () => CODECS["OrgMemberList.members"]}}, "required": ["members"]},
  "OrgMemberList.members": {"kind": "array", "items": () => CODECS["OrgMember"]},
  "Project": {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "description": {"ts": "description"}, "id": {"ts": "id"}, "name": {"ts": "name"}, "org_id": {"ts": "orgId"}, "repository_url": {"ts": "repositoryUrl"}, "slug": {"ts": "slug"}, "status": {"ts": "status"}, "updated_at": {"ts": "updatedAt"}}, "required": ["created_at", "created_by", "description", "id", "name", "org_id", "repository_url", "slug", "status", "updated_at"]},
  "ProjectList": {"kind": "object", "fields": {"projects": {"ts": "projects", "codec": () => CODECS["ProjectList.projects"]}}, "required": ["projects"]},
  "ProjectList.projects": {"kind": "array", "items": () => CODECS["Project"]},
  "ProviderStatus": {"kind": "object", "fields": {"capabilities": {"ts": "capabilities", "codec": () => CODECS["ProviderStatus.capabilities"]}, "name": {"ts": "name"}, "region": {"ts": "region"}, "version": {"ts": "version"}}, "required": ["capabilities", "name", "region", "version"]},
  "ProviderStatus.capabilities": {"kind": "array"},
  "PublicConfig": {"kind": "object", "fields": {"app_id": {"ts": "appId"}, "auth_base_path": {"ts": "authBasePath"}, "publishable_key": {"ts": "publishableKey"}}, "required": ["app_id", "auth_base_path", "publishable_key"]},
  "Session": {"kind": "object", "fields": {"org_id": {"ts": "orgId"}, "permissions": {"ts": "permissions", "codec": () => CODECS["Session.permissions"]}, "user_id": {"ts": "userId"}}, "required": ["org_id", "permissions", "user_id"]},
  "Session.permissions": {"kind": "array"},
  "UpdateProjectInputBody": {"kind": "object", "fields": {"description": {"ts": "description"}, "name": {"ts": "name"}, "repository_url": {"ts": "repositoryUrl"}}},
};

/** Convert a wire payload into its TypeScript shape. */
export function decode<T>(value: unknown, codecRef?: CodecRef): T {
  return decodeRef<T>(value, typeof codecRef === 'string' ? CODECS[codecRef] : codecRef);
}

/** Convert a TypeScript value into its wire shape. */
export function encode(value: unknown, codecRef?: CodecRef): unknown {
  return encodeRef(value, typeof codecRef === 'string' ? CODECS[codecRef] : codecRef);
}
