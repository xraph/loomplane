import type { Codec } from '../codec-runtime';
import { codec_DNSRecord } from './DNSRecord';
import { codec_RouteBinding } from './RouteBinding';
import { codec_RouteInput } from './RouteInput';
import { codec_RouteVerification } from './RouteVerification';

export const codec_CloudinfraRoute: Codec = {"kind": "object", "fields": {"binding": {"ts": "binding", "codec": () => codec_RouteBinding}, "id": {"ts": "id"}, "input": {"ts": "input", "codec": () => codec_RouteInput}, "operation_id": {"ts": "operationId"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "receipt": {"ts": "receipt", "codec": () => codec_DNSRecord}, "revision": {"ts": "revision"}, "status": {"ts": "status"}, "updated_at": {"ts": "updatedAt"}, "verification": {"ts": "verification", "codec": () => codec_RouteVerification}}, "required": ["binding", "id", "input", "operation_id", "org_id", "project_id", "revision", "status", "updated_at", "verification"]};
