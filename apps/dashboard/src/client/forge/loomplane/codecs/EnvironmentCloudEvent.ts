import type { Codec } from '../codec-runtime';

export const codec_EnvironmentCloudEvent: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "environment_id": {"ts": "environmentId"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "revision": {"ts": "revision"}}, "required": ["cloud_id", "environment_id", "org_id", "project_id", "revision"]};
