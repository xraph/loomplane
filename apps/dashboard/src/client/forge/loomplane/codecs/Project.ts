import type { Codec } from '../codec-runtime';

export const codec_Project: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "description": {"ts": "description"}, "id": {"ts": "id"}, "name": {"ts": "name"}, "org_id": {"ts": "orgId"}, "repository_url": {"ts": "repositoryUrl"}, "slug": {"ts": "slug"}, "status": {"ts": "status"}, "updated_at": {"ts": "updatedAt"}}, "required": ["created_at", "created_by", "description", "id", "name", "org_id", "repository_url", "slug", "status", "updated_at"]};
