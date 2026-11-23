import type { Codec } from '../codec-runtime';
import { codec_RunnerView_evidence } from './RunnerView.evidence';

export const codec_RunnerView: Codec = {"kind": "object", "fields": {"architecture": {"ts": "architecture"}, "builds": {"ts": "builds"}, "checked_at": {"ts": "checkedAt"}, "created_at": {"ts": "createdAt"}, "evidence": {"ts": "evidence", "codec": () => codec_RunnerView_evidence}, "last_build_at": {"ts": "lastBuildAt"}, "name": {"ts": "name"}, "node_id": {"ts": "nodeId"}, "org_id": {"ts": "orgId"}, "pool_id": {"ts": "poolId"}, "reason": {"ts": "reason"}, "runtime_identity": {"ts": "runtimeIdentity"}, "status": {"ts": "status"}, "takes_builds": {"ts": "takesBuilds"}}, "required": ["builds", "created_at", "name", "node_id", "org_id", "pool_id", "status", "takes_builds"]};
