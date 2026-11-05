import type { Codec } from '../codec-runtime';

export const codec_FleetOperation: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "error_code": {"ts": "errorCode"}, "generation": {"ts": "generation"}, "id": {"ts": "id"}, "instance_id": {"ts": "instanceId"}, "kind": {"ts": "kind"}, "request_hash": {"ts": "requestHash"}, "status": {"ts": "status"}}, "required": ["created_at", "generation", "id", "instance_id", "kind", "request_hash", "status"]};
