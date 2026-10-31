import type { Codec } from '../codec-runtime';
import { codec_Plan_actions } from './Plan.actions';
import { codec_Plan_artifacts } from './Plan.artifacts';
import { codec_Plan_connections } from './Plan.connections';
import { codec_Plan_leftovers } from './Plan.leftovers';
import { codec_Plan_problems } from './Plan.problems';
import { codec_Plan_sources } from './Plan.sources';
import { codec_Plan_warnings } from './Plan.warnings';
import { codec_PlanningSummary } from './PlanningSummary';
import { codec_Release } from './Release';
import { codec_Rollback } from './Rollback';

export const codec_Plan: Codec = {"kind": "object", "fields": {"actions": {"ts": "actions", "codec": () => codec_Plan_actions}, "artifacts": {"ts": "artifacts", "codec": () => codec_Plan_artifacts}, "catalog": {"ts": "catalog"}, "cloud_sync_id": {"ts": "cloudSyncId"}, "connections": {"ts": "connections", "codec": () => codec_Plan_connections}, "created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "decided_at": {"ts": "decidedAt"}, "decided_by": {"ts": "decidedBy"}, "decision_note": {"ts": "decisionNote"}, "environment": {"ts": "environment"}, "environment_id": {"ts": "environmentId"}, "error": {"ts": "error"}, "executable": {"ts": "executable"}, "expires_at": {"ts": "expiresAt"}, "from_environment": {"ts": "fromEnvironment"}, "from_environment_id": {"ts": "fromEnvironmentId"}, "id": {"ts": "id"}, "kind": {"ts": "kind"}, "leftovers": {"ts": "leftovers", "codec": () => codec_Plan_leftovers}, "manifest_id": {"ts": "manifestId"}, "manifest_revision": {"ts": "manifestRevision"}, "org_id": {"ts": "orgId"}, "planned_at": {"ts": "plannedAt"}, "problems": {"ts": "problems", "codec": () => codec_Plan_problems}, "project_id": {"ts": "projectId"}, "release": {"ts": "release", "codec": () => codec_Release}, "required_runtime_protocol": {"ts": "requiredRuntimeProtocol"}, "revision": {"ts": "revision"}, "rollback": {"ts": "rollback", "codec": () => codec_Rollback}, "sources": {"ts": "sources", "codec": () => codec_Plan_sources}, "status": {"ts": "status"}, "summary": {"ts": "summary", "codec": () => codec_PlanningSummary}, "warnings": {"ts": "warnings", "codec": () => codec_Plan_warnings}}, "required": ["actions", "artifacts", "catalog", "connections", "created_at", "created_by", "decided_by", "decision_note", "environment", "environment_id", "error", "executable", "from_environment", "from_environment_id", "id", "kind", "leftovers", "manifest_id", "manifest_revision", "org_id", "problems", "project_id", "revision", "sources", "status", "summary", "warnings"]};
