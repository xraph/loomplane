import type { Codec } from '../codec-runtime';
import { codec_LocationPolicy_preferred_locations } from './LocationPolicy.preferred_locations';
import { codec_LocationPolicy_preferred_providers } from './LocationPolicy.preferred_providers';

export const codec_LocationPolicy: Codec = {"kind": "object", "fields": {"preferred_locations": {"ts": "preferredLocations", "codec": () => codec_LocationPolicy_preferred_locations}, "preferred_providers": {"ts": "preferredProviders", "codec": () => codec_LocationPolicy_preferred_providers}, "ranking": {"ts": "ranking"}, "revision": {"ts": "revision"}}, "required": ["revision"]};
