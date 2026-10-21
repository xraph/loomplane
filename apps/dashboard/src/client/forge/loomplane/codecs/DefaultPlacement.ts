import type { Codec } from '../codec-runtime';

export const codec_DefaultPlacement: Codec = {"kind": "object", "fields": {"connection_id": {"ts": "connectionId"}, "provider": {"ts": "provider"}, "region": {"ts": "region"}}, "required": ["connection_id", "provider", "region"]};
