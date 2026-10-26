import type { Codec } from '../codec-runtime';

export const codec_Thresholds: Codec = {"kind": "object", "fields": {"error_rate": {"ts": "errorRate"}, "error_rate_delta": {"ts": "errorRateDelta"}, "min_requests": {"ts": "minRequests"}, "p95_latency_ratio": {"ts": "p95LatencyRatio"}}, "required": ["error_rate", "error_rate_delta", "min_requests", "p95_latency_ratio"]};
