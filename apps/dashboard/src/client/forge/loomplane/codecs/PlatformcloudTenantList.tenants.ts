import type { Codec } from '../codec-runtime';
import { codec_TenantRow } from './TenantRow';

export const codec_PlatformcloudTenantList_tenants: Codec = {"kind": "array", "items": () => codec_TenantRow};
