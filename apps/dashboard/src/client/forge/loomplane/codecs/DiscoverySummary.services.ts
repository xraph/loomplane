import type { Codec } from '../codec-runtime';
import { codec_DiscoveredService } from './DiscoveredService';

export const codec_DiscoverySummary_services: Codec = {"kind": "array", "items": () => codec_DiscoveredService};
