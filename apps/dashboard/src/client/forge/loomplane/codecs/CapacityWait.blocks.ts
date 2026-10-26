import type { Codec } from '../codec-runtime';
import { codec_CapacityBlock } from './CapacityBlock';

export const codec_CapacityWait_blocks: Codec = {"kind": "array", "items": () => codec_CapacityBlock};
