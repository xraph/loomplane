import type { Codec } from '../codec-runtime';
import { codec_RetainedPoolStorage } from './RetainedPoolStorage';

export const codec_ServerUsage_retained_storage: Codec = {"kind": "array", "items": () => codec_RetainedPoolStorage};
