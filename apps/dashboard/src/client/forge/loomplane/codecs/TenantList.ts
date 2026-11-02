import type { Codec } from '../codec-runtime';
import { codec_TenantList_tenants } from './TenantList.tenants';

export const codec_TenantList: Codec = {"kind": "object", "fields": {"tenants": {"ts": "tenants", "codec": () => codec_TenantList_tenants}}, "required": ["tenants"]};
