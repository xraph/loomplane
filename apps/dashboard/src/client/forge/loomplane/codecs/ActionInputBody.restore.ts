import type { Codec } from '../codec-runtime';

export const codec_ActionInputBody_restore: Codec = {"kind": "object", "fields": {"recovery_point_id": {"ts": "recoveryPointId"}, "target_profile_id": {"ts": "targetProfileId"}, "target_stage_id": {"ts": "targetStageId"}}, "required": ["recovery_point_id", "target_profile_id", "target_stage_id"]};
