import type { Codec } from '../codec-runtime';

export const codec_ServerDefaults: Codec = {"kind": "object", "fields": {"base_url": {"ts": "baseUrl"}, "key_set": {"ts": "keySet"}, "model": {"ts": "model"}, "provider": {"ts": "provider"}}, "required": ["key_set", "model", "provider"]};
