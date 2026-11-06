import type { Codec } from '../codec-runtime';
import { codec_ProviderEnvironment } from './ProviderEnvironment';

export const codec_ProviderEnvironmentList_providerEnvironments: Codec = {"kind": "array", "items": () => codec_ProviderEnvironment};
