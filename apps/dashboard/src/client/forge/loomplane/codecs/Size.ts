import type { Codec } from '../codec-runtime';

export const codec_Size: Codec = {"kind": "object", "fields": {"cpu": {"ts": "cpu"}, "memory": {"ts": "memory"}}, "required": ["cpu", "memory"]};
