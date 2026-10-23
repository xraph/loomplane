import type { Codec } from '../codec-runtime';
import { codec_Binding } from './Binding';
import { codec_CloudCompensation_routes } from './CloudCompensation.routes';

export const codec_CloudCompensation: Codec = {"kind": "object", "fields": {"action_id": {"ts": "actionId"}, "candidate": {"ts": "candidate", "codec": () => codec_Binding}, "original_completed": {"ts": "originalCompleted"}, "plan_id": {"ts": "planId"}, "retained": {"ts": "retained"}, "routes": {"ts": "routes", "codec": () => codec_CloudCompensation_routes}, "server_attempt": {"ts": "serverAttempt"}, "source": {"ts": "source", "codec": () => codec_Binding}}, "required": ["action_id", "candidate", "original_completed", "plan_id", "retained", "server_attempt", "source"]};
