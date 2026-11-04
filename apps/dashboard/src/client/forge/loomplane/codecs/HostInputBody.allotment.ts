import type { Codec } from '../codec-runtime';

export const codec_HostInputBody_allotment: Codec = {"kind": "object", "fields": {"disk_mib": {"ts": "diskMib"}, "memory_mib": {"ts": "memoryMib"}, "vcpus": {"ts": "vcpus"}, "vms": {"ts": "vms"}}, "required": ["disk_mib", "memory_mib", "vcpus", "vms"]};
