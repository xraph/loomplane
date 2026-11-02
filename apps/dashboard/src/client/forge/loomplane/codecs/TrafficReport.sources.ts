import type { Codec } from '../codec-runtime';
import { codec_UsageSource } from './UsageSource';

export const codec_TrafficReport_sources: Codec = {"kind": "array", "items": () => codec_UsageSource};
