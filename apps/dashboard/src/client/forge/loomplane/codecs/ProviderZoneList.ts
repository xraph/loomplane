import type { Codec } from '../codec-runtime';
import { codec_ProviderZoneList_zones } from './ProviderZoneList.zones';

export const codec_ProviderZoneList: Codec = {"kind": "object", "fields": {"zones": {"ts": "zones", "codec": () => codec_ProviderZoneList_zones}}, "required": ["zones"]};
