import type { Codec } from '../codec-runtime';
import { codec_MetricSourceObservation } from './MetricSourceObservation';

export const codec_MetricSummary_sources: Codec = {"kind": "array", "items": () => codec_MetricSourceObservation};
