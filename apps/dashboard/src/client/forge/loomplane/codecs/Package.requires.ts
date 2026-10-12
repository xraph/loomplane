import type { Codec } from '../codec-runtime';
import { codec_Requirement } from './Requirement';

export const codec_Package_requires: Codec = {"kind": "array", "items": () => codec_Requirement};
