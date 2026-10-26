import type { Codec } from '../codec-runtime';

export const codec_ConsumerKey: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "last_used_at": {"ts": "lastUsedAt"}, "last_used_known": {"ts": "lastUsedKnown"}, "prefix": {"ts": "prefix"}, "revoked_at": {"ts": "revokedAt"}, "service": {"ts": "service"}, "state": {"ts": "state"}, "tenant": {"ts": "tenant"}}, "required": ["last_used_known", "service", "state", "tenant"]};
