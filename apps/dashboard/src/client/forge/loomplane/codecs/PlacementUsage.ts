import type { Codec } from '../codec-runtime';
import { codec_PlacementUsage_services } from './PlacementUsage.services';

export const codec_PlacementUsage: Codec = {"kind": "object", "fields": {"default": {"ts": "default"}, "services": {"ts": "services", "codec": () => codec_PlacementUsage_services}, "target_id": {"ts": "targetId"}}, "required": ["services"]};
