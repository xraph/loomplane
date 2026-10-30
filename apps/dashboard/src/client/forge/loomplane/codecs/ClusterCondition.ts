import type { Codec } from '../codec-runtime';

export const codec_ClusterCondition: Codec = {"kind": "object", "fields": {"reason": {"ts": "reason"}, "status": {"ts": "status"}, "type": {"ts": "type"}}, "required": ["status", "type"]};
