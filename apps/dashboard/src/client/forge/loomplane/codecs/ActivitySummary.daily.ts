import type { Codec } from '../codec-runtime';
import { codec_ActivityDay } from './ActivityDay';

export const codec_ActivitySummary_daily: Codec = {"kind": "array", "items": () => codec_ActivityDay};
