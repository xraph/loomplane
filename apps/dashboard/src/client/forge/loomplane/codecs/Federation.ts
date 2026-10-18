import type { Codec } from '../codec-runtime';
import { codec_Federation_access_grants } from './Federation.access_grants';
import { codec_Federation_destinations } from './Federation.destinations';
import { codec_Federation_network_links } from './Federation.network_links';
import { codec_Federation_placements } from './Federation.placements';
import { codec_Federation_service_exports } from './Federation.service_exports';

export const codec_Federation: Codec = {"kind": "object", "fields": {"access_grants": {"ts": "accessGrants", "codec": () => codec_Federation_access_grants}, "destinations": {"ts": "destinations", "codec": () => codec_Federation_destinations}, "network_links": {"ts": "networkLinks", "codec": () => codec_Federation_network_links}, "placements": {"ts": "placements", "codec": () => codec_Federation_placements}, "service_exports": {"ts": "serviceExports", "codec": () => codec_Federation_service_exports}}};
