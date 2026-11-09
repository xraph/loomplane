import type { Codec } from '../codec-runtime';
import { codec_PlatformcloudTenantInputBody_limits } from './PlatformcloudTenantInputBody.limits';

export const codec_PlatformcloudTenantInputBody: Codec = {"kind": "object", "fields": {"limits": {"ts": "limits", "codec": () => codec_PlatformcloudTenantInputBody_limits}, "status": {"ts": "status"}}, "required": ["status"]};
