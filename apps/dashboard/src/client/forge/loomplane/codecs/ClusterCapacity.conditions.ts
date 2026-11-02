import type { Codec } from '../codec-runtime';
import { codec_ClusterCondition } from './ClusterCondition';

export const codec_ClusterCapacity_conditions: Codec = {"kind": "array", "items": () => codec_ClusterCondition};
