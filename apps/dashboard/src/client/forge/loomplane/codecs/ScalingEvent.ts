import type { Codec } from '../codec-runtime';

export const codec_ScalingEvent: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "desired_replicas": {"ts": "desiredReplicas"}, "observed_at": {"ts": "observedAt"}, "policy_revision": {"ts": "policyRevision"}, "previous_replicas": {"ts": "previousReplicas"}, "reason": {"ts": "reason"}, "service": {"ts": "service"}, "writer": {"ts": "writer"}}, "required": ["desired_replicas", "observed_at", "policy_revision", "previous_replicas", "reason", "service", "writer"]};
