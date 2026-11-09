import type { Codec } from '../codec-runtime';

export const codec_WorkspacePlacement: Codec = {"kind": "object", "fields": {"environment": {"ts": "environment"}, "project": {"ts": "project"}, "region": {"ts": "region"}, "service": {"ts": "service"}, "state": {"ts": "state"}, "waiting_capacity": {"ts": "waitingCapacity"}}, "required": ["environment", "project", "region", "service", "state", "waiting_capacity"]};
