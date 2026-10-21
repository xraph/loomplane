import type { Codec } from '../codec-runtime';
import { codec_RetentionPolicyHistory_events } from './RetentionPolicyHistory.events';

export const codec_RetentionPolicyHistory: Codec = {"kind": "object", "fields": {"events": {"ts": "events", "codec": () => codec_RetentionPolicyHistory_events}, "next_before": {"ts": "nextBefore"}}, "required": ["events"]};
