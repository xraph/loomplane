import type { Codec } from '../codec-runtime';

export const codec_CleanupReview: Codec = {"kind": "object", "fields": {"connection_id": {"ts": "connectionId"}, "deployment_id": {"ts": "deploymentId"}, "effect": {"ts": "effect"}, "provider": {"ts": "provider"}, "region": {"ts": "region"}, "resource": {"ts": "resource"}, "revision": {"ts": "revision"}}, "required": ["connection_id", "deployment_id", "effect", "provider", "region", "resource", "revision"]};
