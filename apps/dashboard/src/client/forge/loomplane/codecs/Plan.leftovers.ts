import type { Codec } from '../codec-runtime';
import { codec_Leftover } from './Leftover';

export const codec_Plan_leftovers: Codec = {"kind": "array", "items": () => codec_Leftover};
