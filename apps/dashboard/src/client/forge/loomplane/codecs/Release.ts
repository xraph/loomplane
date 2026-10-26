import type { Codec } from '../codec-runtime';
import { codec_Release_canary } from './Release.canary';
import { codec_Release_steps } from './Release.steps';
import { codec_Thresholds } from './Thresholds';

export const codec_Release: Codec = {"kind": "object", "fields": {"canary": {"ts": "canary", "codec": () => codec_Release_canary}, "hold_seconds": {"ts": "holdSeconds"}, "steps": {"ts": "steps", "codec": () => codec_Release_steps}, "strategy": {"ts": "strategy"}, "thresholds": {"ts": "thresholds", "codec": () => codec_Thresholds}}, "required": ["strategy", "thresholds"]};
