import type { Codec } from '../codec-runtime';
import { codec_EnvironmentChange_services } from './EnvironmentChange.services';
import { codec_PlanSummary } from './PlanSummary';

export const codec_EnvironmentChange: Codec = {"kind": "object", "fields": {"deployment_id": {"ts": "deploymentId"}, "environment": {"ts": "environment"}, "environment_id": {"ts": "environmentId"}, "error": {"ts": "error"}, "external_routing": {"ts": "externalRouting"}, "plan_id": {"ts": "planId"}, "project_id": {"ts": "projectId"}, "routing_evidence": {"ts": "routingEvidence"}, "services": {"ts": "services", "codec": () => codec_EnvironmentChange_services}, "status": {"ts": "status"}, "summary": {"ts": "summary", "codec": () => codec_PlanSummary}}, "required": ["environment", "environment_id", "external_routing", "services", "status", "summary"]};
