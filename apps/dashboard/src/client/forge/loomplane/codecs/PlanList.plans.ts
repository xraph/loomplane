import type { Codec } from '../codec-runtime';
import { codec_Plan } from './Plan';

export const codec_PlanList_plans: Codec = {"kind": "array", "items": () => codec_Plan};
