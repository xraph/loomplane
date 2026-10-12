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
  "AcceptInvitationRequest": {"kind": "object", "fields": {"token": {"ts": "token"}}, "required": ["token"]},
  "AddMemberRequest": {"kind": "object", "fields": {"role": {"ts": "role"}, "user_id": {"ts": "userId"}}, "required": ["user_id"]},
  "CreateInvitationRequest": {"kind": "object", "fields": {"email": {"ts": "email"}, "role": {"ts": "role"}}, "required": ["email"]},
  "CreateOrgRequest": {"kind": "object", "fields": {"app_id": {"ts": "appId"}, "logo": {"ts": "logo"}, "name": {"ts": "name"}, "slug": {"ts": "slug"}}, "required": ["name", "slug"]},
  "DeclineInvitationRequest": {"kind": "object", "fields": {"token": {"ts": "token"}}, "required": ["token"]},
  "Invitation": {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "email": {"ts": "email"}, "expires_at": {"ts": "expiresAt"}, "id": {"ts": "id"}, "inviter_id": {"ts": "inviterId"}, "org_id": {"ts": "orgId"}, "role": {"ts": "role"}, "status": {"ts": "status"}}, "required": ["created_at", "email", "expires_at", "id", "inviter_id", "org_id", "role", "status"]},
  "InvitationListResponse": {"kind": "object", "fields": {"invitations": {"ts": "invitations", "codec": () => CODECS["InvitationListResponse.invitations"]}, "next_cursor": {"ts": "nextCursor"}}, "required": ["invitations"]},
  "InvitationListResponse.invitations": {"kind": "array", "items": () => CODECS["Invitation"]},
  "Member": {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "id": {"ts": "id"}, "org_id": {"ts": "orgId"}, "role": {"ts": "role"}, "updated_at": {"ts": "updatedAt"}, "user_id": {"ts": "userId"}}, "required": ["created_at", "id", "org_id", "role", "updated_at", "user_id"]},
  "MemberListResponse": {"kind": "object", "fields": {"members": {"ts": "members", "codec": () => CODECS["MemberListResponse.members"]}, "next_cursor": {"ts": "nextCursor"}}, "required": ["members"]},
  "MemberListResponse.members": {"kind": "array", "items": () => CODECS["Member"]},
  "OrgListResponse": {"kind": "object", "fields": {"next_cursor": {"ts": "nextCursor"}, "organizations": {"ts": "organizations", "codec": () => CODECS["OrgListResponse.organizations"]}}, "required": ["organizations"]},
  "OrgListResponse.organizations": {"kind": "array", "items": () => CODECS["Organization"]},
  "Organization": {"kind": "object", "fields": {"app_id": {"ts": "appId"}, "created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "env_id": {"ts": "envId"}, "id": {"ts": "id"}, "logo": {"ts": "logo"}, "metadata": {"ts": "metadata", "codec": () => CODECS["Organization.metadata"]}, "name": {"ts": "name"}, "slug": {"ts": "slug"}, "updated_at": {"ts": "updatedAt"}}, "required": ["app_id", "created_at", "created_by", "env_id", "id", "name", "slug", "updated_at"]},
  "Organization.metadata": {"kind": "record"},
  "OrganizationStatusResponse": {"kind": "object", "fields": {"status": {"ts": "status"}}, "required": ["status"]},
  "SlugAvailableResponse": {"kind": "object", "fields": {"available": {"ts": "available"}}, "required": ["available"]},
  "SwitchOrgRequest": {"kind": "object", "fields": {"org_id": {"ts": "orgId"}}, "required": ["org_id"]},
  "SwitchOrgResponse": {"kind": "object", "fields": {"org_id": {"ts": "orgId"}, "session_id": {"ts": "sessionId"}}, "required": ["session_id"]},
  "UpdateMemberRequest": {"kind": "object", "fields": {"role": {"ts": "role"}}, "required": ["role"]},
  "UpdateOrgRequest": {"kind": "object", "fields": {"logo": {"ts": "logo"}, "name": {"ts": "name"}}},
};

/** Convert a wire payload into its TypeScript shape. */
export function decode<T>(value: unknown, codecRef?: CodecRef): T {
  return decodeRef<T>(value, typeof codecRef === 'string' ? CODECS[codecRef] : codecRef);
}

/** Convert a TypeScript value into its wire shape. */
export function encode(value: unknown, codecRef?: CodecRef): unknown {
  return encodeRef(value, typeof codecRef === 'string' ? CODECS[codecRef] : codecRef);
}
