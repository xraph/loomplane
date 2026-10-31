import type { Codec } from '../codec-runtime';

export const codec_ManagedCleanupBinding: Codec = {"kind": "object", "fields": {"connection_id": {"ts": "connectionId"}, "external_id": {"ts": "externalId"}, "id": {"ts": "id"}, "plan_id": {"ts": "planId"}, "revision": {"ts": "revision"}, "updated_at": {"ts": "updatedAt"}}, "required": ["connection_id", "external_id", "id", "plan_id", "revision", "updated_at"]};
