import type { Codec } from '../codec-runtime';
import { codec_Canary } from './Canary';
import { codec_CanaryReport_verdicts } from './CanaryReport.verdicts';

export const codec_CanaryReport: Codec = {"kind": "object", "fields": {"canary": {"ts": "canary", "codec": () => codec_Canary}, "verdicts": {"ts": "verdicts", "codec": () => codec_CanaryReport_verdicts}}, "required": ["verdicts"]};
