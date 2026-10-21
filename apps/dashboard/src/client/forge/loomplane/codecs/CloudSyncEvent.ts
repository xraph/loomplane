import type { Codec } from '../codec-runtime';
import { codec_CloudSyncEvent_environments } from './CloudSyncEvent.environments';

export const codec_CloudSyncEvent: Codec = {"kind": "object", "fields": {"attempt": {"ts": "attempt"}, "cloud_id": {"ts": "cloudId"}, "environments": {"ts": "environments", "codec": () => codec_CloudSyncEvent_environments}, "id": {"ts": "id"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "reverts": {"ts": "reverts"}, "status": {"ts": "status"}, "target_id": {"ts": "targetId"}, "updated_at": {"ts": "updatedAt"}}, "required": ["attempt", "cloud_id", "environments", "id", "org_id", "project_id", "status", "target_id", "updated_at"]};
