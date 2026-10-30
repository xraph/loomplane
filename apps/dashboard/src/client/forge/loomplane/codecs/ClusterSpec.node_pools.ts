import type { Codec } from '../codec-runtime';
import { codec_NodePoolSpec } from './NodePoolSpec';

export const codec_ClusterSpec_node_pools: Codec = {"kind": "array", "items": () => codec_NodePoolSpec};
