import type { Codec } from '../codec-runtime';

export const codec_DiscoverySource: Codec = {"kind": "object", "fields": {"alias": {"ts": "alias"}, "created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "id": {"ts": "id"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "ref": {"ts": "ref"}, "removed_at": {"ts": "removedAt"}, "repository": {"ts": "repository"}, "root": {"ts": "root"}, "updated_at": {"ts": "updatedAt"}}, "required": ["alias", "created_at", "created_by", "id", "org_id", "project_id", "ref", "repository", "root", "updated_at"]};
