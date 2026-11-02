import type { Codec } from '../codec-runtime';

export const codec_ServerInputBody: Codec = {"kind": "object", "fields": {"acknowledge_unknown_costs": {"ts": "acknowledgeUnknownCosts"}, "cloud_revision": {"ts": "cloudRevision"}, "confirm_adoption": {"ts": "confirmAdoption"}, "connection_id": {"ts": "connectionId"}, "image": {"ts": "image"}, "kind": {"ts": "kind"}, "name": {"ts": "name"}, "region": {"ts": "region"}, "server_id": {"ts": "serverId"}, "server_revision": {"ts": "serverRevision"}, "size": {"ts": "size"}}, "required": ["cloud_revision", "kind"]};
