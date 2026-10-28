import type { Codec } from '../codec-runtime';

export const codec_CapacityChange: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "entity_id": {"ts": "entityId"}, "kind": {"ts": "kind"}, "org_id": {"ts": "orgId"}, "revision": {"ts": "revision"}, "status": {"ts": "status"}}, "required": ["cloud_id", "entity_id", "kind", "org_id", "revision"]};
