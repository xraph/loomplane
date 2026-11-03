import type { Codec } from '../codec-runtime';
import { codec_NodeJoined_endpoints } from './NodeJoined.endpoints';

export const codec_NodeJoined: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "endpoints": {"ts": "endpoints", "codec": () => codec_NodeJoined_endpoints}, "identity_generation": {"ts": "identityGeneration"}, "node_id": {"ts": "nodeId"}, "org_id": {"ts": "orgId"}}, "required": ["endpoints", "identity_generation", "node_id", "org_id"]};
