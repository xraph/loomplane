import type { Codec } from '../codec-runtime';
import { codec_PlatformcloudTenantList_tenants } from './PlatformcloudTenantList.tenants';

export const codec_PlatformcloudTenantList: Codec = {"kind": "object", "fields": {"tenants": {"ts": "tenants", "codec": () => codec_PlatformcloudTenantList_tenants}}, "required": ["tenants"]};
