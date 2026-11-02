import type { Codec } from '../codec-runtime';
import { codec_Point } from './Point';

export const codec_AccountSpend_days: Codec = {"kind": "array", "items": () => codec_Point};
