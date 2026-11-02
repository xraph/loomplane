import type { Codec } from '../codec-runtime';
import { codec_Canary_services } from './Canary.services';
import { codec_Canary_steps } from './Canary.steps';
import { codec_Thresholds } from './Thresholds';

export const codec_Canary: Codec = {"kind": "object", "fields": {"hold_seconds": {"ts": "holdSeconds"}, "reason": {"ts": "reason"}, "services": {"ts": "services", "codec": () => codec_Canary_services}, "status": {"ts": "status"}, "step": {"ts": "step"}, "steps": {"ts": "steps", "codec": () => codec_Canary_steps}, "thresholds": {"ts": "thresholds", "codec": () => codec_Thresholds}, "updated_at": {"ts": "updatedAt"}, "weight": {"ts": "weight"}}, "required": ["hold_seconds", "services", "status", "step", "steps", "thresholds", "updated_at", "weight"]};
