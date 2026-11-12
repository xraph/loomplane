import type { Codec } from '../codec-runtime';
import { codec_ScalingPolicy_signals } from './ScalingPolicy.signals';

export const codec_ScalingPolicy: Codec = {"kind": "object", "fields": {"cooldown_seconds": {"ts": "cooldownSeconds"}, "cpu_utilization": {"ts": "cpuUtilization"}, "downscale_stabilization_seconds": {"ts": "downscaleStabilizationSeconds"}, "max_replicas": {"ts": "maxReplicas"}, "min_replicas": {"ts": "minReplicas"}, "mode": {"ts": "mode"}, "revision": {"ts": "revision"}, "scale_up_limit": {"ts": "scaleUpLimit"}, "signals": {"ts": "signals", "codec": () => codec_ScalingPolicy_signals}, "version": {"ts": "version"}}, "required": ["max_replicas", "min_replicas", "mode", "revision", "version"]};
