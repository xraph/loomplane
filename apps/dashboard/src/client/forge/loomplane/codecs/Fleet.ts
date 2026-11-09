import type { Codec } from '../codec-runtime';

export const codec_Fleet: Codec = {"kind": "object", "fields": {"control_project_id": {"ts": "controlProjectId"}, "created_at": {"ts": "createdAt"}, "id": {"ts": "id"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "revision": {"ts": "revision"}, "status": {"ts": "status"}, "updated_at": {"ts": "updatedAt"}}, "required": ["created_at", "id", "org_id", "project_id", "revision", "status", "updated_at"]};
