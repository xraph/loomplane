import type { Codec } from '../codec-runtime';

export const codec_PlatformLimits: Codec = {"kind": "object", "fields": {"concurrent_builds": {"ts": "concurrentBuilds"}, "memory_mib": {"ts": "memoryMib"}, "vcpus": {"ts": "vcpus"}, "vms": {"ts": "vms"}}, "required": ["concurrent_builds", "memory_mib", "vcpus", "vms"]};
