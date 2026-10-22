import type { Codec } from '../codec-runtime';
import { codec_IdentitySession_permissions } from './IdentitySession.permissions';

export const codec_IdentitySession: Codec = {"kind": "object", "fields": {"org_id": {"ts": "orgId"}, "permissions": {"ts": "permissions", "codec": () => codec_IdentitySession_permissions}, "user_id": {"ts": "userId"}}, "required": ["org_id", "permissions", "user_id"]};
