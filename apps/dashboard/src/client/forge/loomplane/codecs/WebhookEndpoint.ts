import type { Codec } from '../codec-runtime';

export const codec_WebhookEndpoint: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "description": {"ts": "description"}, "enabled": {"ts": "enabled"}, "id": {"ts": "id"}, "url": {"ts": "url"}}, "required": ["created_at", "description", "enabled", "id", "url"]};
