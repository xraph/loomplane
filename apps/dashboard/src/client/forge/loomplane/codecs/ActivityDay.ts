import type { Codec } from '../codec-runtime';

export const codec_ActivityDay: Codec = {"kind": "object", "fields": {"date": {"ts": "date"}, "failed": {"ts": "failed"}, "succeeded": {"ts": "succeeded"}}, "required": ["date", "failed", "succeeded"]};
