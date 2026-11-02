import type { Codec } from '../codec-runtime';

export const codec_ExistingResourceConfig: Codec = {"kind": "object", "fields": {"account_identity": {"ts": "accountIdentity"}, "endpoint_id": {"ts": "endpointId"}, "endpoint_revision": {"ts": "endpointRevision"}, "external_id": {"ts": "externalId"}, "incarnation": {"ts": "incarnation"}, "resource_id": {"ts": "resourceId"}, "revision": {"ts": "revision"}}, "required": ["account_identity", "endpoint_id", "endpoint_revision", "external_id", "incarnation", "resource_id", "revision"]};
