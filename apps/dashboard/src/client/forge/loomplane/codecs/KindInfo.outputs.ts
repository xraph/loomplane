import type { Codec } from '../codec-runtime';
import { codec_OutputInfo } from './OutputInfo';

export const codec_KindInfo_outputs: Codec = {"kind": "array", "items": () => codec_OutputInfo};
