import type { Codec } from '../codec-runtime';

export const codec_Health: Codec = {"kind": "object", "fields": {"available": {"ts": "available"}, "errors": {"ts": "errors"}, "latency_ms": {"ts": "latencyMs"}, "reason": {"ts": "reason"}, "requests": {"ts": "requests"}, "statistic": {"ts": "statistic"}}, "required": ["available", "errors", "latency_ms", "requests"]};
