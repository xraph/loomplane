import type { Codec } from '../codec-runtime';
import { codec_CleanupReview_blockers } from './CleanupReview.blockers';
import { codec_CleanupReview_targets } from './CleanupReview.targets';

export const codec_CleanupReview: Codec = {"kind": "object", "fields": {"blockers": {"ts": "blockers", "codec": () => codec_CleanupReview_blockers}, "connection_id": {"ts": "connectionId"}, "deployment_id": {"ts": "deploymentId"}, "effect": {"ts": "effect"}, "previous_review_id": {"ts": "previousReviewId"}, "provider": {"ts": "provider"}, "region": {"ts": "region"}, "resource": {"ts": "resource"}, "review_sequence": {"ts": "reviewSequence"}, "revision": {"ts": "revision"}, "targets": {"ts": "targets", "codec": () => codec_CleanupReview_targets}, "variant": {"ts": "variant"}}, "required": ["connection_id", "deployment_id", "effect", "provider", "region", "resource", "revision"]};
