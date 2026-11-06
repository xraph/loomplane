import type { Codec } from '../codec-runtime';
import { codec_ProviderEnvironmentList_provider_environments } from './ProviderEnvironmentList.provider_environments';

export const codec_ProviderEnvironmentList: Codec = {"kind": "object", "fields": {"provider_environments": {"ts": "providerEnvironments", "codec": () => codec_ProviderEnvironmentList_provider_environments}}, "required": ["provider_environments"]};
