import type { Codec } from '../codec-runtime';
import { codec_PlatformLimits } from './PlatformLimits';
import { codec_PlatformcloudTenant_suspend_reasons } from './PlatformcloudTenant.suspend_reasons';

export const codec_PlatformcloudTenant: Codec = {"kind": "object", "fields": {"limits": {"ts": "limits", "codec": () => codec_PlatformLimits}, "org_id": {"ts": "orgId"}, "revision": {"ts": "revision"}, "status": {"ts": "status"}, "suspend_reasons": {"ts": "suspendReasons", "codec": () => codec_PlatformcloudTenant_suspend_reasons}, "suspended_at": {"ts": "suspendedAt"}}, "required": ["org_id", "revision", "status", "suspend_reasons"]};
