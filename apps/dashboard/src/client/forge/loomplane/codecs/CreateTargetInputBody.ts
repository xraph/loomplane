import type { Codec } from '../codec-runtime';

export const codec_CreateTargetInputBody: Codec = {"kind": "object", "fields": {"connection_id": {"ts": "connectionId"}, "display_name": {"ts": "displayName"}, "name": {"ts": "name"}, "provider": {"ts": "provider"}, "region": {"ts": "region"}}, "required": ["connection_id", "display_name", "name", "provider", "region"]};
