import type { Codec } from '../codec-runtime';
import { codec_PlanSource } from './PlanSource';

export const codec_Plan_sources: Codec = {"kind": "array", "items": () => codec_PlanSource};
