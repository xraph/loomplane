import type { Codec } from '../codec-runtime';

export const codec_RetentionInput: Codec = {"kind": "object", "fields": {"keep_last": {"ts": "keepLast"}, "older_than_days": {"ts": "olderThanDays"}}, "required": ["keep_last", "older_than_days"]};
