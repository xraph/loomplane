import type { Codec } from '../codec-runtime';

export const codec_CloudAdoption: Codec = {"kind": "object", "fields": {"binding_id": {"ts": "bindingId"}, "config_digest": {"ts": "configDigest"}, "external_id": {"ts": "externalId"}, "plan_id": {"ts": "planId"}, "revision": {"ts": "revision"}}, "required": ["binding_id", "config_digest", "external_id", "plan_id", "revision"]};
