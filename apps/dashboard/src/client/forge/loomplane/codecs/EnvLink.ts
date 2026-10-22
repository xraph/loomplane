import type { Codec } from '../codec-runtime';

export const codec_EnvLink: Codec = {"kind": "object", "fields": {"address": {"ts": "address"}, "field": {"ts": "field"}, "from": {"ts": "from"}, "key": {"ts": "key"}, "subject": {"ts": "subject"}}, "required": ["from", "key", "subject"]};
