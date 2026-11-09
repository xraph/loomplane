import type { Codec } from '../codec-runtime';
import { codec_SourceDiff } from './SourceDiff';

export const codec_ReadinessEvidence_sources: Codec = {"kind": "array", "items": () => codec_SourceDiff};
