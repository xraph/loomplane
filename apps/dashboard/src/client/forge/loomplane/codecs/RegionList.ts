import type { Codec } from '../codec-runtime';
import { codec_RegionList_regions } from './RegionList.regions';

export const codec_RegionList: Codec = {"kind": "object", "fields": {"regions": {"ts": "regions", "codec": () => codec_RegionList_regions}}, "required": ["regions"]};
