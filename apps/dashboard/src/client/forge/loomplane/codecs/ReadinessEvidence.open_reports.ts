import type { Codec } from '../codec-runtime';
import { codec_OpenReport } from './OpenReport';

export const codec_ReadinessEvidence_open_reports: Codec = {"kind": "array", "items": () => codec_OpenReport};
