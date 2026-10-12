import type { Codec } from '../codec-runtime';

export const codec_APIKey: Codec = {"kind": "object", "fields": {"access": {"ts": "access"}, "created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "expires_at": {"ts": "expiresAt"}, "hint": {"ts": "hint"}, "id": {"ts": "id"}, "last_used_at": {"ts": "lastUsedAt"}, "name": {"ts": "name"}, "revoked_at": {"ts": "revokedAt"}, "state": {"ts": "state"}}, "required": ["access", "created_at", "created_by", "hint", "id", "name", "state"]};
