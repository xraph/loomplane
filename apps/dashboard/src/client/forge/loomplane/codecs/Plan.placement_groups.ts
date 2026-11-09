import type { Codec } from '../codec-runtime';
import { codec_PlacementGroupReview } from './PlacementGroupReview';

export const codec_Plan_placement_groups: Codec = {"kind": "array", "items": () => codec_PlacementGroupReview};
