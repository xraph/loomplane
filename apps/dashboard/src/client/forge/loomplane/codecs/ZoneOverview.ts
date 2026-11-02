import type { Codec } from '../codec-runtime';
import { codec_Zone } from './Zone';
import { codec_ZoneOverview_hostnames } from './ZoneOverview.hostnames';
import { codec_ZoneOverview_subdomains } from './ZoneOverview.subdomains';

export const codec_ZoneOverview: Codec = {"kind": "object", "fields": {"hostnames": {"ts": "hostnames", "codec": () => codec_ZoneOverview_hostnames}, "parent_zone_id": {"ts": "parentZoneId"}, "proof_zone_id": {"ts": "proofZoneId"}, "registration_id": {"ts": "registrationId"}, "state": {"ts": "state"}, "subdomains": {"ts": "subdomains", "codec": () => codec_ZoneOverview_subdomains}, "wildcards": {"ts": "wildcards"}, "zone": {"ts": "zone", "codec": () => codec_Zone}}, "required": ["hostnames", "state", "subdomains", "wildcards"]};
