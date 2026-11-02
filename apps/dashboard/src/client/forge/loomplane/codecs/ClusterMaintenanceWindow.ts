import type { Codec } from '../codec-runtime';

export const codec_ClusterMaintenanceWindow: Codec = {"kind": "object", "fields": {"duration_seconds": {"ts": "durationSeconds"}, "end_utc": {"ts": "endUtc"}, "local_start": {"ts": "localStart"}, "start_utc": {"ts": "startUtc"}, "timezone": {"ts": "timezone"}}, "required": ["duration_seconds", "end_utc", "local_start", "start_utc", "timezone"]};
