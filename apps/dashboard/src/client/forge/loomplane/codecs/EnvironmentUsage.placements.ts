import type { Codec } from '../codec-runtime';
import { codec_PlacementUsage } from './PlacementUsage';

export const codec_EnvironmentUsage_placements: Codec = {"kind": "array", "items": () => codec_PlacementUsage};
