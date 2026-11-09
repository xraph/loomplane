import type { Codec } from '../codec-runtime';
import { codec_ResourceSetting } from './ResourceSetting';

export const codec_Provisions_settings: Codec = {"kind": "array", "items": () => codec_ResourceSetting};
