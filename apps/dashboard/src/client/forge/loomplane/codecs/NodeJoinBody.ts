import type { Codec } from '../codec-runtime';
import { codec_NodeJoinBody_endpoints } from './NodeJoinBody.endpoints';
import { codec_NodeJoinBody_inventory } from './NodeJoinBody.inventory';
import { codec_NodeJoinBody_labels } from './NodeJoinBody.labels';

export const codec_NodeJoinBody: Codec = {"kind": "object", "fields": {"endpoints": {"ts": "endpoints", "codec": () => codec_NodeJoinBody_endpoints}, "inventory": {"ts": "inventory", "codec": () => codec_NodeJoinBody_inventory}, "labels": {"ts": "labels", "codec": () => codec_NodeJoinBody_labels}, "name": {"ts": "name"}, "node_version": {"ts": "nodeVersion"}, "protocol_version": {"ts": "protocolVersion"}, "public_key": {"ts": "publicKey"}, "token": {"ts": "token"}}, "required": ["inventory", "node_version", "protocol_version", "public_key", "token"]};
