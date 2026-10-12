import type { Codec } from '../codec-runtime';

export const codec_Member: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "id": {"ts": "id"}, "org_id": {"ts": "orgId"}, "role": {"ts": "role"}, "updated_at": {"ts": "updatedAt"}, "user_id": {"ts": "userId"}}, "required": ["created_at", "id", "org_id", "role", "updated_at", "user_id"]};
