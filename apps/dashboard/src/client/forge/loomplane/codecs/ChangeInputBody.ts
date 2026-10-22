import type { Codec } from '../codec-runtime';

export const codec_ChangeInputBody: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "connection_id": {"ts": "connectionId"}, "default": {"ts": "default"}, "environment_id": {"ts": "environmentId"}, "pool_id": {"ts": "poolId"}, "provider": {"ts": "provider"}, "region": {"ts": "region"}, "revision": {"ts": "revision"}, "runtime": {"ts": "runtime"}, "target_id": {"ts": "targetId"}}, "required": ["cloud_id", "revision"]};
