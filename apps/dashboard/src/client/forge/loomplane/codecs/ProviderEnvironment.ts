import type { Codec } from '../codec-runtime';

export const codec_ProviderEnvironment: Codec = {"kind": "object", "fields": {"createdAt": {"ts": "createdAt"}, "environmentId": {"ts": "environmentId"}, "mode": {"ts": "mode"}, "permanent": {"ts": "permanent"}, "provider": {"ts": "provider"}, "reason": {"ts": "reason"}, "scope": {"ts": "scope"}}, "required": ["createdAt", "environmentId", "mode", "permanent", "provider", "reason", "scope"]};
