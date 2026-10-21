import type { Codec } from '../codec-runtime';
import { codec_Pool } from './Pool';

export const codec_PoolList_pools: Codec = {"kind": "array", "items": () => codec_Pool};
