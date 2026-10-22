import type { Codec } from '../codec-runtime';
import { codec_Assignment_targets } from './Assignment.targets';
import { codec_DefaultPlacement } from './DefaultPlacement';

export const codec_Assignment: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "cloud_revision": {"ts": "cloudRevision"}, "default_placement": {"ts": "defaultPlacement", "codec": () => codec_DefaultPlacement}, "environment_id": {"ts": "environmentId"}, "grant_revision": {"ts": "grantRevision"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "revision": {"ts": "revision"}, "targets": {"ts": "targets", "codec": () => codec_Assignment_targets}}, "required": ["cloud_id", "cloud_revision", "environment_id", "grant_revision", "org_id", "project_id", "revision", "targets"]};
