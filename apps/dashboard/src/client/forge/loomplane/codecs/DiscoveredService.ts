import type { Codec } from '../codec-runtime';
import { codec_DiscoveredService_metadata } from './DiscoveredService.metadata';

export const codec_DiscoveredService: Codec = {"kind": "object", "fields": {"error": {"ts": "error"}, "metadata": {"ts": "metadata", "codec": () => codec_DiscoveredService_metadata}, "opted_in": {"ts": "optedIn"}, "routes": {"ts": "routes"}, "service": {"ts": "service"}, "state": {"ts": "state"}, "updated_at": {"ts": "updatedAt"}}, "required": ["opted_in", "routes", "service", "state", "updated_at"]};
