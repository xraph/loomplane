import type { Codec } from '../codec-runtime';

export const codec_ClusterChange: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "operation_id": {"ts": "operationId"}, "reason": {"ts": "reason"}, "resource_id": {"ts": "resourceId"}, "revision": {"ts": "revision"}, "status": {"ts": "status"}}, "required": ["cloud_id", "operation_id", "reason", "resource_id", "revision", "status"]};
