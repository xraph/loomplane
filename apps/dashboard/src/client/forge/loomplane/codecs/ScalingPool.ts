import type { Codec } from '../codec-runtime';
import { codec_CapacityPolicy } from './CapacityPolicy';
import { codec_Pool } from './Pool';

export const codec_ScalingPool: Codec = {"kind": "object", "fields": {"policy": {"ts": "policy", "codec": () => codec_CapacityPolicy}, "pool": {"ts": "pool", "codec": () => codec_Pool}}};
