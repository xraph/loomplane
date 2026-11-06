import type { Codec } from '../codec-runtime';
import { codec_ProviderEnvironmentList_providerEnvironments } from './ProviderEnvironmentList.providerEnvironments';

export const codec_ProviderEnvironmentList: Codec = {"kind": "object", "fields": {"providerEnvironments": {"ts": "providerEnvironments", "codec": () => codec_ProviderEnvironmentList_providerEnvironments}}, "required": ["providerEnvironments"]};
