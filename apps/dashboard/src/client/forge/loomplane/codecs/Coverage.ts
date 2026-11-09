import type { Codec } from '../codec-runtime';
import { codec_Coverage_canary } from './Coverage.canary';
import { codec_Coverage_services } from './Coverage.services';

export const codec_Coverage: Codec = {"kind": "object", "fields": {"canary": {"ts": "canary", "codec": () => codec_Coverage_canary}, "duration": {"ts": "duration"}, "environment": {"ts": "environment"}, "services": {"ts": "services", "codec": () => codec_Coverage_services}, "unverified": {"ts": "unverified"}}, "required": ["canary", "duration", "environment", "services", "unverified"]};
