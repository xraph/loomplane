import type { Codec } from '../codec-runtime';

export const codec_NativeRetirementInputBody_policy: Codec = {"kind": "object", "fields": {"keep_last": {"ts": "keepLast"}, "older_than_days": {"ts": "olderThanDays"}}, "required": ["keep_last", "older_than_days"]};
