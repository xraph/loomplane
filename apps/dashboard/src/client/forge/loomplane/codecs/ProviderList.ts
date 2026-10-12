import type { Codec } from '../codec-runtime';
import { codec_ProviderList_providers } from './ProviderList.providers';

export const codec_ProviderList: Codec = {"kind": "object", "fields": {"providers": {"ts": "providers", "codec": () => codec_ProviderList_providers}}, "required": ["providers"]};
