import type { Codec } from '../codec-runtime';

export const codec_InfrastructureAction: Codec = {"kind": "object", "fields": {"id": {"ts": "id"}, "kind": {"ts": "kind"}, "resource_id": {"ts": "resourceId"}, "status": {"ts": "status"}}, "required": ["id", "kind", "resource_id", "status"]};
