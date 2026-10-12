import type { Codec } from '../codec-runtime';
import { codec_ProviderStatus_capabilities } from './ProviderStatus.capabilities';

export const codec_ProviderStatus: Codec = {"kind": "object", "fields": {"capabilities": {"ts": "capabilities", "codec": () => codec_ProviderStatus_capabilities}, "name": {"ts": "name"}, "region": {"ts": "region"}, "version": {"ts": "version"}}, "required": ["capabilities", "name", "region", "version"]};
