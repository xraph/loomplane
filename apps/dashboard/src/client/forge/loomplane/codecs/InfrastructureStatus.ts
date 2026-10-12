import type { Codec } from '../codec-runtime';
import { codec_InfrastructureStatus_providers } from './InfrastructureStatus.providers';

export const codec_InfrastructureStatus: Codec = {"kind": "object", "fields": {"engine": {"ts": "engine"}, "providers": {"ts": "providers", "codec": () => codec_InfrastructureStatus_providers}, "store": {"ts": "store"}}, "required": ["engine", "providers", "store"]};
