import type { Codec } from '../codec-runtime';
import { codec_Point } from './Point';

export const codec_TrafficReport_requests: Codec = {"kind": "array", "items": () => codec_Point};
