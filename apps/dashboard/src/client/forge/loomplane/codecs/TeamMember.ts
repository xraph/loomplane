import type { Codec } from '../codec-runtime';

export const codec_TeamMember: Codec = {"kind": "object", "fields": {"added_at": {"ts": "addedAt"}, "email": {"ts": "email"}, "name": {"ts": "name"}, "user_id": {"ts": "userId"}}, "required": ["added_at", "email", "name", "user_id"]};
