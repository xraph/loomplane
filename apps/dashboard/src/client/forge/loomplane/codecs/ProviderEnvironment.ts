import type { Codec } from '../codec-runtime';

export const codec_ProviderEnvironment: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "environment_id": {"ts": "environmentId"}, "mode": {"ts": "mode"}, "permanent": {"ts": "permanent"}, "provider": {"ts": "provider"}, "reason": {"ts": "reason"}, "scope": {"ts": "scope"}}, "required": ["created_at", "environment_id", "mode", "permanent", "provider", "reason", "scope"]};
