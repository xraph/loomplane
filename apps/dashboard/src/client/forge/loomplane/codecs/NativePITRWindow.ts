import type { Codec } from '../codec-runtime';

export const codec_NativePITRWindow: Codec = {"kind": "object", "fields": {"available": {"ts": "available"}, "earliest": {"ts": "earliest"}, "latest": {"ts": "latest"}, "observed_at": {"ts": "observedAt"}, "point_id": {"ts": "pointId"}, "provider": {"ts": "provider"}, "source_id": {"ts": "sourceId"}, "unavailable_reason": {"ts": "unavailableReason"}}, "required": ["available", "provider", "source_id"]};
