import type { Codec } from '../codec-runtime';

export const codec_ChangeInput: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "connection_id": {"ts": "connectionId"}, "environment_id": {"ts": "environmentId"}, "provider": {"ts": "provider"}, "region": {"ts": "region"}, "revision": {"ts": "revision"}, "target_id": {"ts": "targetId"}}, "required": ["cloud_id", "revision"]};
