import type { Codec } from '../codec-runtime';
import { codec_DiscoverySummary_services } from './DiscoverySummary.services';

export const codec_DiscoverySummary: Codec = {"kind": "object", "fields": {"last_change": {"ts": "lastChange"}, "mode": {"ts": "mode"}, "namespace": {"ts": "namespace"}, "services": {"ts": "services", "codec": () => codec_DiscoverySummary_services}}, "required": ["last_change", "mode", "services"]};
