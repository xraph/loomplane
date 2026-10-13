import type { Codec } from '../codec-runtime';
import { codec_ActivitySummary_daily } from './ActivitySummary.daily';
import { codec_AnalysisCounts } from './AnalysisCounts';
import { codec_DeploymentCounts } from './DeploymentCounts';
import { codec_DriftCounts } from './DriftCounts';
import { codec_PlanCounts } from './PlanCounts';

export const codec_ActivitySummary: Codec = {"kind": "object", "fields": {"analyses": {"ts": "analyses", "codec": () => codec_AnalysisCounts}, "daily": {"ts": "daily", "codec": () => codec_ActivitySummary_daily}, "days": {"ts": "days"}, "deployments": {"ts": "deployments", "codec": () => codec_DeploymentCounts}, "drift": {"ts": "drift", "codec": () => codec_DriftCounts}, "plans": {"ts": "plans", "codec": () => codec_PlanCounts}, "since": {"ts": "since"}}, "required": ["analyses", "daily", "days", "deployments", "drift", "plans", "since"]};
