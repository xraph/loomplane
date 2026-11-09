import type { Codec } from '../codec-runtime';
import { codec_CanarySummary_action_ids } from './CanarySummary.action_ids';
import { codec_CanarySummary_services } from './CanarySummary.services';

export const codec_CanarySummary: Codec = {"kind": "object", "fields": {"action_ids": {"ts": "actionIds", "codec": () => codec_CanarySummary_action_ids}, "deployment_id": {"ts": "deploymentId"}, "finished_at": {"ts": "finishedAt"}, "plan_id": {"ts": "planId"}, "reason": {"ts": "reason"}, "services": {"ts": "services", "codec": () => codec_CanarySummary_services}, "status": {"ts": "status"}}, "required": ["action_ids", "deployment_id", "plan_id", "services", "status"]};
