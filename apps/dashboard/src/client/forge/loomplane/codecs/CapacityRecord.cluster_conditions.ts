import type { Codec } from '../codec-runtime';
import { codec_ClusterCondition } from './ClusterCondition';

export const codec_CapacityRecord_cluster_conditions: Codec = {"kind": "array", "items": () => codec_ClusterCondition};
