import type { Codec } from '../codec-runtime';

export const codec_DeploymentCounts: Codec = {"kind": "object", "fields": {"failed": {"ts": "failed"}, "median_seconds": {"ts": "medianSeconds"}, "retried": {"ts": "retried"}, "running": {"ts": "running"}, "succeeded": {"ts": "succeeded"}}, "required": ["failed", "median_seconds", "retried", "running", "succeeded"]};
