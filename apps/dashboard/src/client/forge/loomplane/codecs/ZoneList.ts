import type { Codec } from '../codec-runtime';
import { codec_ZoneList_zones } from './ZoneList.zones';

export const codec_ZoneList: Codec = {"kind": "object", "fields": {"zones": {"ts": "zones", "codec": () => codec_ZoneList_zones}}, "required": ["zones"]};
