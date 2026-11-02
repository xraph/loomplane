import type { Codec } from '../codec-runtime';

export const codec_PoolInputBody_members_items: Codec = {"kind": "object", "fields": {"cpu_millis": {"ts": "cpuMillis"}, "draining": {"ts": "draining"}, "gpus": {"ts": "gpus"}, "memory_bytes": {"ts": "memoryBytes"}, "server_id": {"ts": "serverId"}}, "required": ["cpu_millis", "draining", "memory_bytes", "server_id"]};
