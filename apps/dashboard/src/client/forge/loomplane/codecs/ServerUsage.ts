import type { Codec } from '../codec-runtime';
import { codec_ServerUsage_retained_storage } from './ServerUsage.retained_storage';
import { codec_ServerUsage_workloads } from './ServerUsage.workloads';

export const codec_ServerUsage: Codec = {"kind": "object", "fields": {"draining": {"ts": "draining"}, "reserved_cpu_millis": {"ts": "reservedCpuMillis"}, "reserved_gpus": {"ts": "reservedGpus"}, "reserved_memory_bytes": {"ts": "reservedMemoryBytes"}, "retained_storage": {"ts": "retainedStorage", "codec": () => codec_ServerUsage_retained_storage}, "server_id": {"ts": "serverId"}, "workloads": {"ts": "workloads", "codec": () => codec_ServerUsage_workloads}}, "required": ["draining", "reserved_cpu_millis", "reserved_gpus", "reserved_memory_bytes", "retained_storage", "server_id", "workloads"]};
