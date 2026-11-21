import type { Codec } from '../codec-runtime';

export const codec_ProjectCloudEvent: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}}, "required": ["cloud_id", "org_id", "project_id"]};
