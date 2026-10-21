import type { Codec } from '../codec-runtime';
import { codec_Sync_environments } from './Sync.environments';
import { codec_Target } from './Target';

export const codec_Sync: Codec = {"kind": "object", "fields": {"after": {"ts": "after", "codec": () => codec_Target}, "attempt": {"ts": "attempt"}, "before": {"ts": "before", "codec": () => codec_Target}, "cloud_id": {"ts": "cloudId"}, "created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "environments": {"ts": "environments", "codec": () => codec_Sync_environments}, "error": {"ts": "error"}, "id": {"ts": "id"}, "org_id": {"ts": "orgId"}, "preview_id": {"ts": "previewId"}, "project_id": {"ts": "projectId"}, "reverts": {"ts": "reverts"}, "status": {"ts": "status"}, "target_id": {"ts": "targetId"}, "updated_at": {"ts": "updatedAt"}}, "required": ["attempt", "cloud_id", "created_at", "created_by", "environments", "id", "org_id", "preview_id", "project_id", "status", "updated_at"]};
