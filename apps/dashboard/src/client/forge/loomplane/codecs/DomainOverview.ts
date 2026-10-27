import type { Codec } from '../codec-runtime';
import { codec_DomainOverview_registrations } from './DomainOverview.registrations';
import { codec_DomainOverview_zones } from './DomainOverview.zones';

export const codec_DomainOverview: Codec = {"kind": "object", "fields": {"registrations": {"ts": "registrations", "codec": () => codec_DomainOverview_registrations}, "zones": {"ts": "zones", "codec": () => codec_DomainOverview_zones}}, "required": ["registrations", "zones"]};
