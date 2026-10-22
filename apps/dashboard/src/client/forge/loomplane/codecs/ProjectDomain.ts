import type { Codec } from '../codec-runtime';
import { codec_ProjectDomain_hostnames } from './ProjectDomain.hostnames';

export const codec_ProjectDomain: Codec = {"kind": "object", "fields": {"hostnames": {"ts": "hostnames", "codec": () => codec_ProjectDomain_hostnames}, "mode": {"ts": "mode"}, "name": {"ts": "name"}, "provider": {"ts": "provider"}, "verified": {"ts": "verified"}, "wildcards": {"ts": "wildcards"}, "zone_id": {"ts": "zoneId"}}, "required": ["hostnames", "mode", "name", "verified", "wildcards", "zone_id"]};
