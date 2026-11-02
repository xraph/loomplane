import type { Codec } from '../codec-runtime';
import { codec_UsageSource } from './UsageSource';

export const codec_SpendReport_sources: Codec = {"kind": "array", "items": () => codec_UsageSource};
