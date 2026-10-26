import type { Codec } from '../codec-runtime';
import { codec_Amount } from './Amount';

export const codec_SpendReport_totals: Codec = {"kind": "array", "items": () => codec_Amount};
