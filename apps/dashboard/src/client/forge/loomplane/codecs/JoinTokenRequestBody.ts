import type { Codec } from '../codec-runtime';
import { codec_JoinTokenRequestBody_endpoints } from './JoinTokenRequestBody.endpoints';
import { codec_JoinTokenRequestBody_labels } from './JoinTokenRequestBody.labels';

export const codec_JoinTokenRequestBody: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "edge": {"ts": "edge"}, "edge_scope": {"ts": "edgeScope"}, "endpoints": {"ts": "endpoints", "codec": () => codec_JoinTokenRequestBody_endpoints}, "labels": {"ts": "labels", "codec": () => codec_JoinTokenRequestBody_labels}, "lifetime_minutes": {"ts": "lifetimeMinutes"}}};
