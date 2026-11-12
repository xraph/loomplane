import type { Codec } from '../codec-runtime';

export const codec_MetricSourceObservation: Codec = {"kind": "object", "fields": {"component": {"ts": "component"}, "observed_at": {"ts": "observedAt"}, "reason": {"ts": "reason"}, "replicas_observed_at": {"ts": "replicasObservedAt"}, "runtime_identity": {"ts": "runtimeIdentity"}, "runtime_live": {"ts": "runtimeLive"}, "runtime_observed_at": {"ts": "runtimeObservedAt"}, "source": {"ts": "source"}}, "required": ["component", "runtime_live", "source"]};
