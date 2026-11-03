import type { Codec } from '../codec-runtime';
import { codec_ProjectPermissions_permissions } from './ProjectPermissions.permissions';

export const codec_ProjectPermissions: Codec = {"kind": "object", "fields": {"permissions": {"ts": "permissions", "codec": () => codec_ProjectPermissions_permissions}}, "required": ["permissions"]};
