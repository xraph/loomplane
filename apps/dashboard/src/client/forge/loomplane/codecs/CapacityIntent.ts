import type { Codec } from '../codec-runtime';
import { codec_CapacityDemand } from './CapacityDemand';
import { codec_CapacityTemplatePin } from './CapacityTemplatePin';
import { codec_PoolExpansionPolicy } from './PoolExpansionPolicy';

export const codec_CapacityIntent: Codec = {"kind": "object", "fields": {"assignment_revision": {"ts": "assignmentRevision"}, "cloud_revision": {"ts": "cloudRevision"}, "commit_sha": {"ts": "commitSha"}, "demand": {"ts": "demand", "codec": () => codec_CapacityDemand}, "grant_revision": {"ts": "grantRevision"}, "provider_pin": {"ts": "providerPin", "codec": () => codec_CapacityTemplatePin}, "target_name": {"ts": "targetName"}, "target_revision": {"ts": "targetRevision"}, "template": {"ts": "template", "codec": () => codec_PoolExpansionPolicy}, "template_hash": {"ts": "templateHash"}}, "required": ["assignment_revision", "cloud_revision", "commit_sha", "demand", "grant_revision", "provider_pin", "target_name", "target_revision", "template", "template_hash"]};
