import type { Codec } from '../codec-runtime';

export const codec_Environment: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "id": {"ts": "id"}, "kind": {"ts": "kind"}, "name": {"ts": "name"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "updated_at": {"ts": "updatedAt"}}, "required": ["created_at", "id", "kind", "name", "org_id", "project_id", "updated_at"]};
