import type { Codec } from '../codec-runtime';
import { codec_ProviderResourceList_resources } from './ProviderResourceList.resources';
import { codec_ProviderResourceList_supported_providers } from './ProviderResourceList.supported_providers';
import { codec_ProviderResourceList_syncs } from './ProviderResourceList.syncs';

export const codec_ProviderResourceList: Codec = {"kind": "object", "fields": {"resources": {"ts": "resources", "codec": () => codec_ProviderResourceList_resources}, "supported_providers": {"ts": "supportedProviders", "codec": () => codec_ProviderResourceList_supported_providers}, "syncs": {"ts": "syncs", "codec": () => codec_ProviderResourceList_syncs}}, "required": ["resources", "supported_providers", "syncs"]};
