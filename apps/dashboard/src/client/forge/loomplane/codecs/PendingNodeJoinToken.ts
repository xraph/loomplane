import type { Codec } from '../codec-runtime';
import { codec_PendingNodeJoinToken_endpoints } from './PendingNodeJoinToken.endpoints';
import { codec_PendingNodeJoinToken_labels } from './PendingNodeJoinToken.labels';

export const codec_PendingNodeJoinToken: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "created_at": {"ts": "createdAt"}, "edge": {"ts": "edge"}, "edge_scope": {"ts": "edgeScope"}, "endpoints": {"ts": "endpoints", "codec": () => codec_PendingNodeJoinToken_endpoints}, "expires_at": {"ts": "expiresAt"}, "hint": {"ts": "hint"}, "id": {"ts": "id"}, "labels": {"ts": "labels", "codec": () => codec_PendingNodeJoinToken_labels}, "minted_by": {"ts": "mintedBy"}, "minted_by_key_id": {"ts": "mintedByKeyId"}}, "required": ["created_at", "edge", "endpoints", "expires_at", "hint", "id", "minted_by"]};
