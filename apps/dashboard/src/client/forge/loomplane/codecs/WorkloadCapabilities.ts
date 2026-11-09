import type { Codec } from '../codec-runtime';
import { codec_WorkloadCapabilities_scaling_signals } from './WorkloadCapabilities.scaling_signals';

export const codec_WorkloadCapabilities: Codec = {"kind": "object", "fields": {"lifecycle_jobs": {"ts": "lifecycleJobs"}, "pod_groups": {"ts": "podGroups"}, "revision": {"ts": "revision"}, "scaling_signals": {"ts": "scalingSignals", "codec": () => codec_WorkloadCapabilities_scaling_signals}, "scratch_volumes": {"ts": "scratchVolumes"}}, "required": ["lifecycle_jobs", "pod_groups", "revision", "scratch_volumes"]};
