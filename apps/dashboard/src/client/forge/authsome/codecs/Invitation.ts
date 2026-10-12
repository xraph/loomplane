import type { Codec } from '../codec-runtime';

export const codec_Invitation: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "email": {"ts": "email"}, "expires_at": {"ts": "expiresAt"}, "id": {"ts": "id"}, "inviter_id": {"ts": "inviterId"}, "org_id": {"ts": "orgId"}, "role": {"ts": "role"}, "status": {"ts": "status"}}, "required": ["created_at", "email", "expires_at", "id", "inviter_id", "org_id", "role", "status"]};
