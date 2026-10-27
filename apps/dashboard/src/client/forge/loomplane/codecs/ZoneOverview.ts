import type { Codec } from '../codec-runtime';
import { codec_Zone } from './Zone';
import { codec_ZoneOverview_hostnames } from './ZoneOverview.hostnames';

export const codec_ZoneOverview: Codec = {"kind": "object", "fields": {"hostnames": {"ts": "hostnames", "codec": () => codec_ZoneOverview_hostnames}, "registration_id": {"ts": "registrationId"}, "state": {"ts": "state"}, "wildcards": {"ts": "wildcards"}, "zone": {"ts": "zone", "codec": () => codec_Zone}}, "required": ["hostnames", "state", "wildcards"]};
