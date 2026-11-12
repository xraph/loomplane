import type { Codec } from '../codec-runtime';

export const codec_MetricCoverage: Codec = {"kind": "object", "fields": {"cpu": {"ts": "cpu"}, "expected": {"ts": "expected"}, "fresh": {"ts": "fresh"}, "latency": {"ts": "latency"}, "memory": {"ts": "memory"}, "observed": {"ts": "observed"}, "replicas": {"ts": "replicas"}, "requests": {"ts": "requests"}}, "required": ["cpu", "expected", "fresh", "latency", "memory", "observed", "replicas", "requests"]};
