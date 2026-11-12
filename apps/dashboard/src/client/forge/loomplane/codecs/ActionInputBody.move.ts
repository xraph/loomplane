import type { Codec } from '../codec-runtime';

export const codec_ActionInputBody_move: Codec = {"kind": "object", "fields": {"cutover_mode": {"ts": "cutoverMode"}, "target_cloud_id": {"ts": "targetCloudId"}, "target_region": {"ts": "targetRegion"}}, "required": ["cutover_mode", "target_cloud_id", "target_region"]};
