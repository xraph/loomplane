import type { Codec } from '../codec-runtime';
import { codec_NativeRetirement } from './NativeRetirement';

export const codec_NativeRetirementPage_retirements: Codec = {"kind": "array", "items": () => codec_NativeRetirement};
