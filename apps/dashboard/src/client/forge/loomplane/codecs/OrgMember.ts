import type { Codec } from '../codec-runtime';

export const codec_OrgMember: Codec = {"kind": "object", "fields": {"email": {"ts": "email"}, "id": {"ts": "id"}, "joined_at": {"ts": "joinedAt"}, "member_role": {"ts": "memberRole"}, "name": {"ts": "name"}, "role": {"ts": "role"}, "user_id": {"ts": "userId"}}, "required": ["email", "id", "joined_at", "member_role", "name", "role", "user_id"]};
