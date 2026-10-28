import type { Codec } from '../codec-runtime';
import { codec_FrontendUsage } from './FrontendUsage';

export const codec_CapacityUsage_frontends: Codec = {"kind": "array", "items": () => codec_FrontendUsage};
