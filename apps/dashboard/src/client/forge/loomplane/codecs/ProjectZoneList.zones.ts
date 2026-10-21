import type { Codec } from '../codec-runtime';
import { codec_ProjectZone } from './ProjectZone';

export const codec_ProjectZoneList_zones: Codec = {"kind": "array", "items": () => codec_ProjectZone};
