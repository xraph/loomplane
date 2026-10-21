import type { Codec } from '../codec-runtime';
import { codec_ManagedDatabaseProfile } from './ManagedDatabaseProfile';

export const codec_Provisions_managed_profiles: Codec = {"kind": "array", "items": () => codec_ManagedDatabaseProfile};
