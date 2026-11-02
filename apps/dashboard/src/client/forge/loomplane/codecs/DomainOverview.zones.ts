import type { Codec } from '../codec-runtime';
import { codec_ZoneOverview } from './ZoneOverview';

export const codec_DomainOverview_zones: Codec = {"kind": "array", "items": () => codec_ZoneOverview};
