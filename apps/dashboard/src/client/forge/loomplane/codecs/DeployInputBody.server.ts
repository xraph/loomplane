import type { Codec } from '../codec-runtime';

export const codec_DeployInputBody_server: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "operation_id": {"ts": "operationId"}, "review": {"ts": "review"}}, "required": ["cloud_id", "operation_id", "review"]};
