import type { Codec } from '../codec-runtime';
import { codec_DefaultPlacement } from './DefaultPlacement';
import { codec_ProjectCloud_targets } from './ProjectCloud.targets';

export const codec_ProjectCloud: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "cloud_revision": {"ts": "cloudRevision"}, "default": {"ts": "default"}, "default_placement": {"ts": "defaultPlacement", "codec": () => codec_DefaultPlacement}, "grant_revision": {"ts": "grantRevision"}, "name": {"ts": "name"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "targets": {"ts": "targets", "codec": () => codec_ProjectCloud_targets}}, "required": ["cloud_id", "cloud_revision", "default", "grant_revision", "name", "org_id", "project_id", "targets"]};
