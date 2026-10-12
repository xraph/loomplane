import type { Codec } from '../codec-runtime';
import { codec_Analysis } from './Analysis';

export const codec_AnalysisList_analyses: Codec = {"kind": "array", "items": () => codec_Analysis};
