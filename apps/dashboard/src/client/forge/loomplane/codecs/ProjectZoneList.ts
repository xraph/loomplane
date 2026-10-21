import type { Codec } from '../codec-runtime';
import { codec_ProjectZoneList_zones } from './ProjectZoneList.zones';

export const codec_ProjectZoneList: Codec = {"kind": "object", "fields": {"zones": {"ts": "zones", "codec": () => codec_ProjectZoneList_zones}}, "required": ["zones"]};
