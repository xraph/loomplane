import type { Codec } from '../codec-runtime';

export const codec_EnvLink: Codec = {"kind": "object", "fields": {"address": {"ts": "address"}, "from": {"ts": "from"}, "gateway": {"ts": "gateway"}, "key": {"ts": "key"}, "subject": {"ts": "subject"}}, "required": ["from", "key", "subject"]};
