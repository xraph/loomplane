import type { Codec } from '../codec-runtime';
import { codec_NodePoolSpec } from './NodePoolSpec';

export const codec_ClusterObservation_pools: Codec = {"kind": "array", "items": () => codec_NodePoolSpec};
