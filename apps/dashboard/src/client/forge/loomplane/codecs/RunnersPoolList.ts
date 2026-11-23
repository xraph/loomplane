import type { Codec } from '../codec-runtime';
import { codec_RunnersPoolList_pools } from './RunnersPoolList.pools';

export const codec_RunnersPoolList: Codec = {"kind": "object", "fields": {"pools": {"ts": "pools", "codec": () => codec_RunnersPoolList_pools}}, "required": ["pools"]};
