import type { Codec } from '../codec-runtime';
import { codec_Exclusion } from './Exclusion';

export const codec_Project_exclusions: Codec = {"kind": "array", "items": () => codec_Exclusion};
