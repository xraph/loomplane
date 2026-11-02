import type { Codec } from '../codec-runtime';

export const codec_FrontendUsage: Codec = {"kind": "object", "fields": {"active": {"ts": "active"}, "counted": {"ts": "counted"}, "maximum": {"ts": "maximum"}, "pending": {"ts": "pending"}, "retained": {"ts": "retained"}, "target_id": {"ts": "targetId"}}, "required": ["active", "counted", "pending", "retained", "target_id"]};
