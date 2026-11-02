import type { Codec } from '../codec-runtime';

export const codec_ClusterStep: Codec = {"kind": "object", "fields": {"attempted_at": {"ts": "attemptedAt"}, "completed_at": {"ts": "completedAt"}, "name": {"ts": "name"}, "state": {"ts": "state"}}, "required": ["name", "state"]};
