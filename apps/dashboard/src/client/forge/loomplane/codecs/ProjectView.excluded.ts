import type { Codec } from '../codec-runtime';
import { codec_Excluded } from './Excluded';

export const codec_ProjectView_excluded: Codec = {"kind": "array", "items": () => codec_Excluded};
