import type { Codec } from '../codec-runtime';
import { codec_PlacementGroupMemberReview } from './PlacementGroupMemberReview';

export const codec_PlacementGroupReview_members: Codec = {"kind": "array", "items": () => codec_PlacementGroupMemberReview};
