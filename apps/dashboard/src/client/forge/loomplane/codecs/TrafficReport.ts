import type { Codec } from '../codec-runtime';
import { codec_TrafficReport_requests } from './TrafficReport.requests';
import { codec_TrafficReport_services } from './TrafficReport.services';
import { codec_TrafficReport_sources } from './TrafficReport.sources';

export const codec_TrafficReport: Codec = {"kind": "object", "fields": {"end": {"ts": "end"}, "previous_total": {"ts": "previousTotal"}, "range": {"ts": "range"}, "requests": {"ts": "requests", "codec": () => codec_TrafficReport_requests}, "services": {"ts": "services", "codec": () => codec_TrafficReport_services}, "sources": {"ts": "sources", "codec": () => codec_TrafficReport_sources}, "start": {"ts": "start"}, "step": {"ts": "step"}, "total": {"ts": "total"}}, "required": ["end", "previous_total", "range", "requests", "services", "sources", "start", "step", "total"]};
