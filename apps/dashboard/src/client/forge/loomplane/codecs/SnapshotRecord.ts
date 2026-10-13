import type { Codec } from '../codec-runtime';

export const codec_SnapshotRecord: Codec = {"kind": "object", "fields": {"data": {"ts": "data"}, "entity_id": {"ts": "entityId"}, "event": {"ts": "event"}, "id": {"ts": "id"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "time": {"ts": "time"}}, "required": ["data", "entity_id", "event", "id", "org_id", "time"]};
