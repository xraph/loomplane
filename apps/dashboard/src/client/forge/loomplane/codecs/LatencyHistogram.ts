import type { Codec } from '../codec-runtime';
import { codec_LatencyHistogram_counts } from './LatencyHistogram.counts';
import { codec_LatencyHistogram_upper_bounds_millis } from './LatencyHistogram.upper_bounds_millis';

export const codec_LatencyHistogram: Codec = {"kind": "object", "fields": {"counts": {"ts": "counts", "codec": () => codec_LatencyHistogram_counts}, "upper_bounds_millis": {"ts": "upperBoundsMillis", "codec": () => codec_LatencyHistogram_upper_bounds_millis}}, "required": ["counts", "upper_bounds_millis"]};
