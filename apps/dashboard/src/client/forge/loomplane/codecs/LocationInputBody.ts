import type { Codec } from '../codec-runtime';
import { codec_LocationInputBody_cidrs } from './LocationInputBody.cidrs';
import { codec_LocationInputBody_failure_domains } from './LocationInputBody.failure_domains';
import { codec_LocationInputBody_network_refs } from './LocationInputBody.network_refs';

export const codec_LocationInputBody: Codec = {"kind": "object", "fields": {"account_id": {"ts": "accountId"}, "checked_at": {"ts": "checkedAt"}, "cidrs": {"ts": "cidrs", "codec": () => codec_LocationInputBody_cidrs}, "cloud_revision": {"ts": "cloudRevision"}, "connection_id": {"ts": "connectionId"}, "evidence_revision": {"ts": "evidenceRevision"}, "failure_domains": {"ts": "failureDomains", "codec": () => codec_LocationInputBody_failure_domains}, "id": {"ts": "id"}, "name": {"ts": "name"}, "network_refs": {"ts": "networkRefs", "codec": () => codec_LocationInputBody_network_refs}, "provider": {"ts": "provider"}, "region": {"ts": "region"}, "revision": {"ts": "revision"}}, "required": ["account_id", "checked_at", "cloud_revision", "connection_id", "evidence_revision", "name", "provider", "region", "revision"]};
