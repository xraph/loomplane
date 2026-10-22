import type { Codec } from '../codec-runtime';
import { codec_PoolList_pools } from './PoolList.pools';

export const codec_PoolList: Codec = {"kind": "object", "fields": {"pools": {"ts": "pools", "codec": () => codec_PoolList_pools}}, "required": ["pools"]};
