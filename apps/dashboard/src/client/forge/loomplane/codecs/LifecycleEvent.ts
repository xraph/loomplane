import type { Codec } from '../codec-runtime';

export const codec_LifecycleEvent: Codec = {"kind": "object", "fields": {"reason": {"ts": "reason"}, "state": {"ts": "state"}}, "required": ["state"]};
