import type { Codec } from '../codec-runtime';
import { codec_Assignment_targets } from './Assignment.targets';

export const codec_Assignment: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "cloud_revision": {"ts": "cloudRevision"}, "environment_id": {"ts": "environmentId"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "revision": {"ts": "revision"}, "targets": {"ts": "targets", "codec": () => codec_Assignment_targets}}, "required": ["cloud_id", "cloud_revision", "environment_id", "org_id", "project_id", "revision", "targets"]};
