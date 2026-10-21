import type { Codec } from '../codec-runtime';
import { codec_Pool_members } from './Pool.members';

export const codec_Pool: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "id": {"ts": "id"}, "members": {"ts": "members", "codec": () => codec_Pool_members}, "name": {"ts": "name"}, "org_id": {"ts": "orgId"}, "revision": {"ts": "revision"}}, "required": ["cloud_id", "id", "members", "name", "org_id", "revision"]};
