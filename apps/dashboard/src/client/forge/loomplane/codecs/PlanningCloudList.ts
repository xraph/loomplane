import type { Codec } from '../codec-runtime';
import { codec_PlanningCloudList_clouds } from './PlanningCloudList.clouds';

export const codec_PlanningCloudList: Codec = {"kind": "object", "fields": {"clouds": {"ts": "clouds", "codec": () => codec_PlanningCloudList_clouds}}, "required": ["clouds"]};
