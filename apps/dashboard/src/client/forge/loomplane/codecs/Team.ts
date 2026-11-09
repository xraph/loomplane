import type { Codec } from '../codec-runtime';
import { codec_Team_grants } from './Team.grants';
import { codec_Team_members } from './Team.members';

export const codec_Team: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "description": {"ts": "description"}, "grants": {"ts": "grants", "codec": () => codec_Team_grants}, "id": {"ts": "id"}, "members": {"ts": "members", "codec": () => codec_Team_members}, "name": {"ts": "name"}, "org_id": {"ts": "orgId"}, "slug": {"ts": "slug"}, "updated_at": {"ts": "updatedAt"}}, "required": ["created_at", "description", "grants", "id", "members", "name", "org_id", "slug", "updated_at"]};
