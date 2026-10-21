import type { Codec } from '../codec-runtime';
import { codec_ProjectGrant_target_ids } from './ProjectGrant.target_ids';

export const codec_ProjectGrant: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "revision": {"ts": "revision"}, "revoked": {"ts": "revoked"}, "target_ids": {"ts": "targetIds", "codec": () => codec_ProjectGrant_target_ids}}, "required": ["cloud_id", "org_id", "project_id", "revision", "revoked", "target_ids"]};
