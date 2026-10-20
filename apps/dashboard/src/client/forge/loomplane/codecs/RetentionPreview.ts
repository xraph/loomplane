import type { Codec } from '../codec-runtime';
import { codec_RetentionPreview_points } from './RetentionPreview.points';

export const codec_RetentionPreview: Codec = {"kind": "object", "fields": {"as_of": {"ts": "asOf"}, "complete": {"ts": "complete"}, "eligible_bytes": {"ts": "eligibleBytes"}, "eligible_count": {"ts": "eligibleCount"}, "points": {"ts": "points", "codec": () => codec_RetentionPreview_points}}, "required": ["as_of", "complete", "eligible_bytes", "eligible_count", "points"]};
