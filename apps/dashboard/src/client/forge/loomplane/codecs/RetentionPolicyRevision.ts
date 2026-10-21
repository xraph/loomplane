import type { Codec } from '../codec-runtime';
import { codec_RetentionInput } from './RetentionInput';

export const codec_RetentionPolicyRevision: Codec = {"kind": "object", "fields": {"authorized_by": {"ts": "authorizedBy"}, "created_at": {"ts": "createdAt"}, "enabled": {"ts": "enabled"}, "policy": {"ts": "policy", "codec": () => codec_RetentionInput}, "revision": {"ts": "revision"}}, "required": ["authorized_by", "created_at", "enabled", "policy", "revision"]};
