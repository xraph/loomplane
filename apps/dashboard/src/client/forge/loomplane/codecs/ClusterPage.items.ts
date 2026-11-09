import type { Codec } from '../codec-runtime';
import { codec_ClusterObservation } from './ClusterObservation';

export const codec_ClusterPage_items: Codec = {"kind": "array", "items": () => codec_ClusterObservation};
