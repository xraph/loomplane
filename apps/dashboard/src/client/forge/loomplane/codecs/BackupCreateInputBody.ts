import type { Codec } from '../codec-runtime';

export const codec_BackupCreateInputBody: Codec = {"kind": "object", "fields": {"environment": {"ts": "environment"}, "request_id": {"ts": "requestId"}, "resource": {"ts": "resource"}}, "required": ["environment", "request_id", "resource"]};
