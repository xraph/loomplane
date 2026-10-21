import type { Codec } from '../codec-runtime';

export const codec_ApplyCloudInputBody: Codec = {"kind": "object", "fields": {"idempotency_key": {"ts": "idempotencyKey"}, "preview_id": {"ts": "previewId"}, "revision": {"ts": "revision"}}, "required": ["idempotency_key", "preview_id", "revision"]};
