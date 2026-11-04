import type { Codec } from '../codec-runtime';
import { codec_CanarySummary } from './CanarySummary';

export const codec_ReadinessEvidence_canaries: Codec = {"kind": "array", "items": () => codec_CanarySummary};
