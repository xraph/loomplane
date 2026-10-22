import type { Codec } from '../codec-runtime';

export const codec_ReservationRecovery: Codec = {"kind": "object", "fields": {"active": {"ts": "active"}, "phase": {"ts": "phase"}, "plan_id": {"ts": "planId"}, "review": {"ts": "review"}, "revision": {"ts": "revision"}, "server_id": {"ts": "serverId"}, "service": {"ts": "service"}, "started": {"ts": "started"}, "storage_retained": {"ts": "storageRetained"}}, "required": ["active", "phase", "plan_id", "review", "revision", "server_id", "service", "started", "storage_retained"]};
