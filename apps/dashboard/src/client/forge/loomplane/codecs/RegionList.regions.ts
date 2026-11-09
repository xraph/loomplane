import type { Codec } from '../codec-runtime';
import { codec_Region } from './Region';

export const codec_RegionList_regions: Codec = {"kind": "array", "items": () => codec_Region};
