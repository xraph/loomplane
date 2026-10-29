import type { Codec } from '../codec-runtime';
import { codec_NodePoolSpec } from './NodePoolSpec';

export const codec_ClusterInputBody_spec_node_pools: Codec = {"kind": "array", "items": () => codec_NodePoolSpec};
