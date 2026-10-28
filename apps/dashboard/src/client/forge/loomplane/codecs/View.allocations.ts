import type { Codec } from '../codec-runtime';
import { codec_Allocation } from './Allocation';

export const codec_View_allocations: Codec = {"kind": "array", "items": () => codec_Allocation};
