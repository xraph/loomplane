import type { Codec } from '../codec-runtime';

export const codec_GroupDestinationMappingInputBody: Codec = {"kind": "object", "fields": {"attachment_id": {"ts": "attachmentId"}, "destination_id": {"ts": "destinationId"}, "environment_id": {"ts": "environmentId"}, "expected_revision": {"ts": "expectedRevision"}, "target_id": {"ts": "targetId"}}, "required": ["attachment_id", "destination_id", "environment_id", "expected_revision", "target_id"]};
