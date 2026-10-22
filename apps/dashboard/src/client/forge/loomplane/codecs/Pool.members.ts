import type { Codec } from '../codec-runtime';
import { codec_PoolMember } from './PoolMember';

export const codec_Pool_members: Codec = {"kind": "array", "items": () => codec_PoolMember};
