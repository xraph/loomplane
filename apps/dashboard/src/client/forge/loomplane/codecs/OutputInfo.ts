import type { Codec } from '../codec-runtime';

export const codec_OutputInfo: Codec = {"kind": "object", "fields": {"env_key": {"ts": "envKey"}, "name": {"ts": "name"}, "per_consumer": {"ts": "perConsumer"}}, "required": ["env_key", "name", "per_consumer"]};
