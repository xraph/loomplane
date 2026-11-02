import type { Codec } from '../codec-runtime';
import { codec_RuntimeAccessProfile } from './RuntimeAccessProfile';

export const codec_RuntimeAccessReview: Codec = {"kind": "object", "fields": {"attachment_id": {"ts": "attachmentId"}, "attachment_revision": {"ts": "attachmentRevision"}, "capability_revision": {"ts": "capabilityRevision"}, "namespace": {"ts": "namespace"}, "profile": {"ts": "profile", "codec": () => codec_RuntimeAccessProfile}, "project_id": {"ts": "projectId"}}, "required": ["attachment_id", "profile"]};
