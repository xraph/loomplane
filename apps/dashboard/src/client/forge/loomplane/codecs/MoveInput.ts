import type { Codec } from '../codec-runtime';

export const codec_MoveInput: Codec = {"kind": "object", "fields": {"destination_server_id": {"ts": "destinationServerId"}, "environment_id": {"ts": "environmentId"}, "pool_id": {"ts": "poolId"}, "project_id": {"ts": "projectId"}, "review": {"ts": "review"}, "service": {"ts": "service"}}, "required": ["destination_server_id", "environment_id", "pool_id", "project_id", "service"]};
