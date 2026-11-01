import type { Codec } from '../codec-runtime';

export const codec_CleanupProblem: Codec = {"kind": "object", "fields": {"code": {"ts": "code"}, "message": {"ts": "message"}, "retryable": {"ts": "retryable"}}, "required": ["code", "message", "retryable"]};
