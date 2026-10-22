import type { Codec } from '../codec-runtime';

export const codec_RouteTTLInputBody: Codec = {"kind": "object", "fields": {"external_ttl_seconds": {"ts": "externalTtlSeconds"}, "revision": {"ts": "revision"}}, "required": ["external_ttl_seconds", "revision"]};
