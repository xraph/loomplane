import type { Codec } from '../codec-runtime';

export const codec_Placement: Codec = {"kind": "object", "fields": {"connection_generation": {"ts": "connectionGeneration"}, "connection_id": {"ts": "connectionId"}, "cpu_millis": {"ts": "cpuMillis"}, "memory_bytes": {"ts": "memoryBytes"}, "pool_id": {"ts": "poolId"}, "pool_revision": {"ts": "poolRevision"}, "server_id": {"ts": "serverId"}, "server_revision": {"ts": "serverRevision"}}, "required": ["connection_generation", "connection_id", "cpu_millis", "memory_bytes", "pool_id", "pool_revision", "server_id", "server_revision"]};
