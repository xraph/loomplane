import type { Codec } from '../codec-runtime';
import { codec_PoolView } from './PoolView';

export const codec_RunnersPoolList_pools: Codec = {"kind": "array", "items": () => codec_PoolView};
