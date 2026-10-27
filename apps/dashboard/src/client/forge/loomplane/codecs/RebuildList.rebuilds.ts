import type { Codec } from '../codec-runtime';
import { codec_Rebuild } from './Rebuild';

export const codec_RebuildList_rebuilds: Codec = {"kind": "array", "items": () => codec_Rebuild};
