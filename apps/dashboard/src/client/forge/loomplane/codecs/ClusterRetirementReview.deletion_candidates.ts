import type { Codec } from '../codec-runtime';
import { codec_RetainedResource } from './RetainedResource';

export const codec_ClusterRetirementReview_deletion_candidates: Codec = {"kind": "array", "items": () => codec_RetainedResource};
