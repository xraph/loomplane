import type { Codec } from '../codec-runtime';

export const codec_NativeCopyReviewInputBody: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "request_id": {"ts": "requestId"}, "target_id": {"ts": "targetId"}, "ttl_hours": {"ts": "ttlHours"}}, "required": ["cloud_id", "request_id", "target_id", "ttl_hours"]};
