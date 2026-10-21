import type { Codec } from '../codec-runtime';

export const codec_CreateTargetInputBody: Codec = {"kind": "object", "fields": {"connection_id": {"ts": "connectionId"}, "display_name": {"ts": "displayName"}, "name": {"ts": "name"}, "pool_id": {"ts": "poolId"}, "provider": {"ts": "provider"}, "region": {"ts": "region"}, "runtime": {"ts": "runtime"}}, "required": ["connection_id", "display_name", "name", "provider", "region", "runtime"]};
