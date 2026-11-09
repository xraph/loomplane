import type { Codec } from '../codec-runtime';
import { codec_IssuesStatus } from './IssuesStatus';

export const codec_AgentwatchStatus: Codec = {"kind": "object", "fields": {"issues": {"ts": "issues", "codec": () => codec_IssuesStatus}, "paused": {"ts": "paused"}, "paused_at": {"ts": "pausedAt"}, "paused_reason": {"ts": "pausedReason"}, "ticked_at": {"ts": "tickedAt"}}, "required": ["issues", "paused"]};
