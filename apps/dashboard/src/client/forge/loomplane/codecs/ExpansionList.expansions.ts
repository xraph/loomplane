import type { Codec } from '../codec-runtime';
import { codec_Expansion } from './Expansion';

export const codec_ExpansionList_expansions: Codec = {"kind": "array", "items": () => codec_Expansion};
