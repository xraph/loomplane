import type { Codec } from '../codec-runtime';

export const codec_Delivery: Codec = {"kind": "object", "fields": {"attempts": {"ts": "attempts"}, "created_at": {"ts": "createdAt"}, "delivered_at": {"ts": "deliveredAt"}, "event_type": {"ts": "eventType"}, "id": {"ts": "id"}, "next_retry_at": {"ts": "nextRetryAt"}, "status": {"ts": "status"}, "status_code": {"ts": "statusCode"}}, "required": ["attempts", "created_at", "event_type", "id", "status", "status_code"]};
