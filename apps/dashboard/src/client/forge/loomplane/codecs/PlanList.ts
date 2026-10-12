import type { Codec } from '../codec-runtime';
import { codec_PlanList_plans } from './PlanList.plans';

export const codec_PlanList: Codec = {"kind": "object", "fields": {"plans": {"ts": "plans", "codec": () => codec_PlanList_plans}}, "required": ["plans"]};
