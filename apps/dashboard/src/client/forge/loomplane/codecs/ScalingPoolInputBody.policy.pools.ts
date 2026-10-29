import type { Codec } from '../codec-runtime';
import { codec_PoolExpansionPolicy } from './PoolExpansionPolicy';

export const codec_ScalingPoolInputBody_policy_pools: Codec = {"kind": "array", "items": () => codec_PoolExpansionPolicy};
