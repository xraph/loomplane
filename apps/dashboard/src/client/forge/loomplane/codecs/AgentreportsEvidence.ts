import type { Codec } from '../codec-runtime';
import { codec_AgentreportsEvidence_verdict_ids } from './AgentreportsEvidence.verdict_ids';

export const codec_AgentreportsEvidence: Codec = {"kind": "object", "fields": {"analysis_id": {"ts": "analysisId"}, "binding_id": {"ts": "bindingId"}, "deployment_id": {"ts": "deploymentId"}, "plan_id": {"ts": "planId"}, "source_alias": {"ts": "sourceAlias"}, "verdict_ids": {"ts": "verdictIds", "codec": () => codec_AgentreportsEvidence_verdict_ids}, "watch_id": {"ts": "watchId"}}};
