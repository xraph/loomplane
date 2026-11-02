import type { Codec } from '../codec-runtime';
import { codec_APIKey } from './APIKey';
import { codec_CompromisedRevoke_joined_nodes } from './CompromisedRevoke.joined_nodes';

export const codec_CompromisedRevoke: Codec = {"kind": "object", "fields": {"joined_nodes": {"ts": "joinedNodes", "codec": () => codec_CompromisedRevoke_joined_nodes}, "revoked": {"ts": "revoked", "codec": () => codec_APIKey}}, "required": ["joined_nodes", "revoked"]};
