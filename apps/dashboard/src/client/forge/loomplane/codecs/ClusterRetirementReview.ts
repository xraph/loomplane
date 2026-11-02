import type { Codec } from '../codec-runtime';
import { codec_ClusterRetirementReview_blockers } from './ClusterRetirementReview.blockers';
import { codec_ClusterRetirementReview_deletion_candidates } from './ClusterRetirementReview.deletion_candidates';
import { codec_ClusterRetirementReview_retained_resources } from './ClusterRetirementReview.retained_resources';
import { codec_ClusterRetirementReview_unknown_costs } from './ClusterRetirementReview.unknown_costs';

export const codec_ClusterRetirementReview: Codec = {"kind": "object", "fields": {"blockers": {"ts": "blockers", "codec": () => codec_ClusterRetirementReview_blockers}, "checked_at": {"ts": "checkedAt"}, "deletion_candidates": {"ts": "deletionCandidates", "codec": () => codec_ClusterRetirementReview_deletion_candidates}, "digest": {"ts": "digest"}, "estimated_remaining_hourly_micro_usd": {"ts": "estimatedRemainingHourlyMicroUsd"}, "resource_id": {"ts": "resourceId"}, "retained_resources": {"ts": "retainedResources", "codec": () => codec_ClusterRetirementReview_retained_resources}, "unknown_costs": {"ts": "unknownCosts", "codec": () => codec_ClusterRetirementReview_unknown_costs}}, "required": ["blockers", "checked_at", "deletion_candidates", "digest", "estimated_remaining_hourly_micro_usd", "resource_id", "retained_resources", "unknown_costs"]};
