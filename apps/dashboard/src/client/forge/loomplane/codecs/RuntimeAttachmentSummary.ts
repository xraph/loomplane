import type { Codec } from '../codec-runtime';
import { codec_RuntimeAccessProfile } from './RuntimeAccessProfile';

export const codec_RuntimeAttachmentSummary: Codec = {"kind": "object", "fields": {"capability_revision": {"ts": "capabilityRevision"}, "id": {"ts": "id"}, "namespace": {"ts": "namespace"}, "profile": {"ts": "profile", "codec": () => codec_RuntimeAccessProfile}, "project_id": {"ts": "projectId"}, "revision": {"ts": "revision"}, "state": {"ts": "state"}, "target_id": {"ts": "targetId"}}, "required": ["capability_revision", "id", "namespace", "project_id", "revision", "state"]};
