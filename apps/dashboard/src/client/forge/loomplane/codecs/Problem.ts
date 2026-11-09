import type { Codec } from '../codec-runtime';

export const codec_Problem: Codec = {"kind": "object", "fields": {"code": {"ts": "code"}, "message": {"ts": "message"}}, "required": ["code", "message"]};
