import type { Codec } from '../codec-runtime';
import { codec_DNSRecord } from './DNSRecord';
import { codec_EdgePolicy } from './EdgePolicy';
import { codec_EdgeState_urls } from './EdgeState.urls';

export const codec_EdgeState: Codec = {"kind": "object", "fields": {"applied_at": {"ts": "appliedAt"}, "desired": {"ts": "desired", "codec": () => codec_DNSRecord}, "invalidation_error": {"ts": "invalidationError"}, "kind": {"ts": "kind"}, "last_deployment_id": {"ts": "lastDeploymentId"}, "last_purge_at": {"ts": "lastPurgeAt"}, "message": {"ts": "message"}, "org_id": {"ts": "orgId"}, "policy": {"ts": "policy", "codec": () => codec_EdgePolicy}, "prior": {"ts": "prior", "codec": () => codec_DNSRecord}, "project_id": {"ts": "projectId"}, "requested_at": {"ts": "requestedAt"}, "revision": {"ts": "revision"}, "route_id": {"ts": "routeId"}, "status": {"ts": "status"}, "urls": {"ts": "urls", "codec": () => codec_EdgeState_urls}}, "required": ["applied_at", "kind", "last_purge_at", "message", "org_id", "policy", "project_id", "requested_at", "revision", "route_id", "status"]};
