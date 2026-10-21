import type { Codec } from '../codec-runtime';
import { codec_Profile } from './Profile';

export const codec_PlanningCloudList_clouds: Codec = {"kind": "array", "items": () => codec_Profile};
