import type { Codec } from '../codec-runtime';
import { codec_CleanupTarget_objects } from './CleanupTarget.objects';

export const codec_CleanupTarget: Codec = {"kind": "object", "fields": {"deployment_id": {"ts": "deploymentId"}, "identity_current": {"ts": "identityCurrent"}, "kind": {"ts": "kind"}, "namespace": {"ts": "namespace"}, "objects": {"ts": "objects", "codec": () => codec_CleanupTarget_objects}, "plan_id": {"ts": "planId"}, "service": {"ts": "service"}, "unresolved_writes": {"ts": "unresolvedWrites"}}, "required": ["deployment_id", "identity_current", "kind", "namespace", "objects", "plan_id", "service", "unresolved_writes"]};
