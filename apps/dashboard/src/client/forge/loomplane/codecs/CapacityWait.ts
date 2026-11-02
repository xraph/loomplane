import type { Codec } from '../codec-runtime';
import { codec_CapacityWait_blocks } from './CapacityWait.blocks';

export const codec_CapacityWait: Codec = {"kind": "object", "fields": {"blocks": {"ts": "blocks", "codec": () => codec_CapacityWait_blocks}, "created_at": {"ts": "createdAt"}, "revision": {"ts": "revision"}}, "required": ["blocks", "created_at", "revision"]};
