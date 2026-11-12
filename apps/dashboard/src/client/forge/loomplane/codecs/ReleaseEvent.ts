import type { Codec } from '../codec-runtime';

export const codec_ReleaseEvent: Codec = {"kind": "object", "fields": {"campaign_id": {"ts": "campaignId"}, "reason": {"ts": "reason"}, "state": {"ts": "state"}, "target_generation": {"ts": "targetGeneration"}, "target_operation_id": {"ts": "targetOperationId"}, "wave": {"ts": "wave"}}, "required": ["campaign_id", "state", "wave"]};
