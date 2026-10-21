import type { Codec } from '../codec-runtime';
import { codec_RetirementHistory_events } from './RetirementHistory.events';

export const codec_RetirementHistory: Codec = {"kind": "object", "fields": {"events": {"ts": "events", "codec": () => codec_RetirementHistory_events}, "next_before": {"ts": "nextBefore"}}, "required": ["events"]};
