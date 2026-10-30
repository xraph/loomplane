import type { Codec } from '../codec-runtime';
import { codec_ClusterStep } from './ClusterStep';

export const codec_CloudClusterOperation_steps: Codec = {"kind": "array", "items": () => codec_ClusterStep};
