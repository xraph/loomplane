import type { Codec } from '../codec-runtime';
import { codec_ClusterNodeSize } from './ClusterNodeSize';

export const codec_ClusterOptions_node_size_facts: Codec = {"kind": "array", "items": () => codec_ClusterNodeSize};
