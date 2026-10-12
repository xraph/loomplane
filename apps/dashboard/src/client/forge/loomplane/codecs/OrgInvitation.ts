import type { Codec } from '../codec-runtime';

export const codec_OrgInvitation: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "email": {"ts": "email"}, "expires_at": {"ts": "expiresAt"}, "id": {"ts": "id"}, "invited_by": {"ts": "invitedBy"}, "role": {"ts": "role"}}, "required": ["created_at", "email", "expires_at", "id", "invited_by", "role"]};
