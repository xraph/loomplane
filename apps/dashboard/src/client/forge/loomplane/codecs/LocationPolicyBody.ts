import type { Codec } from '../codec-runtime';
import { codec_LocationPolicyBody_preferred_locations } from './LocationPolicyBody.preferred_locations';
import { codec_LocationPolicyBody_preferred_providers } from './LocationPolicyBody.preferred_providers';

export const codec_LocationPolicyBody: Codec = {"kind": "object", "fields": {"preferred_locations": {"ts": "preferredLocations", "codec": () => codec_LocationPolicyBody_preferred_locations}, "preferred_providers": {"ts": "preferredProviders", "codec": () => codec_LocationPolicyBody_preferred_providers}, "ranking": {"ts": "ranking"}, "revision": {"ts": "revision"}}, "required": ["revision"]};
