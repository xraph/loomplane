import type { Codec } from '../codec-runtime';

export const codec_PlanCounts: Codec = {"kind": "object", "fields": {"approved": {"ts": "approved"}, "expired": {"ts": "expired"}, "failed": {"ts": "failed"}, "rejected": {"ts": "rejected"}, "superseded": {"ts": "superseded"}, "waiting": {"ts": "waiting"}}, "required": ["approved", "expired", "failed", "rejected", "superseded", "waiting"]};
