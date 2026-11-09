import type { Codec } from '../codec-runtime';
import { codec_CleanupBlock } from './CleanupBlock';
import { codec_PlatformLimits } from './PlatformLimits';
import { codec_PlatformcloudTenant } from './PlatformcloudTenant';
import { codec_PlatformcloudUse } from './PlatformcloudUse';

export const codec_TenantRow: Codec = {"kind": "object", "fields": {"cleanup_blocked": {"ts": "cleanupBlocked", "codec": () => codec_CleanupBlock}, "limits": {"ts": "limits", "codec": () => codec_PlatformLimits}, "org_id": {"ts": "orgId"}, "org_name": {"ts": "orgName"}, "tenant": {"ts": "tenant", "codec": () => codec_PlatformcloudTenant}, "use": {"ts": "use", "codec": () => codec_PlatformcloudUse}}, "required": ["limits", "org_id", "org_name", "use"]};
