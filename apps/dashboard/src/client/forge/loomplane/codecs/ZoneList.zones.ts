import type { Codec } from '../codec-runtime';
import { codec_Zone } from './Zone';

export const codec_ZoneList_zones: Codec = {"kind": "array", "items": () => codec_Zone};
