import type { Codec } from '../codec-runtime';
import { codec_ClientClusterNodeCapacity } from './ClientClusterNodeCapacity';

export const codec_ClusterCapacity_nodes: Codec = {"kind": "array", "items": () => codec_ClientClusterNodeCapacity};
