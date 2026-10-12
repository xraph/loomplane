import type { Codec } from '../codec-runtime';
import { codec_ProviderStatus } from './ProviderStatus';

export const codec_InfrastructureStatus_providers: Codec = {"kind": "array", "items": () => codec_ProviderStatus};
