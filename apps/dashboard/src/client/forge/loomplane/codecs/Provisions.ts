import type { Codec } from '../codec-runtime';
import { codec_Provisions_managed_profiles } from './Provisions.managed_profiles';
import { codec_Provisions_versions } from './Provisions.versions';

export const codec_Provisions: Codec = {"kind": "object", "fields": {"creates": {"ts": "creates"}, "kind": {"ts": "kind"}, "managed_profiles": {"ts": "managedProfiles", "codec": () => codec_Provisions_managed_profiles}, "versions": {"ts": "versions", "codec": () => codec_Provisions_versions}}, "required": ["creates", "kind"]};
