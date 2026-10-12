import type { Codec } from '../codec-runtime';
import { codec_Session_permissions } from './Session.permissions';

export const codec_Session: Codec = {"kind": "object", "fields": {"org_id": {"ts": "orgId"}, "permissions": {"ts": "permissions", "codec": () => codec_Session_permissions}, "user_id": {"ts": "userId"}}, "required": ["org_id", "permissions", "user_id"]};
