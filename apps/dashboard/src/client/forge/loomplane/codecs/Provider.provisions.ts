import type { Codec } from '../codec-runtime';
import { codec_Provisions } from './Provisions';

export const codec_Provider_provisions: Codec = {"kind": "array", "items": () => codec_Provisions};
