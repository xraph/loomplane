import type { Codec } from '../codec-runtime';
import { codec_CanaryVerdict } from './CanaryVerdict';

export const codec_CanaryReport_verdicts: Codec = {"kind": "array", "items": () => codec_CanaryVerdict};
