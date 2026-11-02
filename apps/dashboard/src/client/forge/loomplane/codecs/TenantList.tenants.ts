import type { Codec } from '../codec-runtime';
import { codec_Tenant } from './Tenant';

export const codec_TenantList_tenants: Codec = {"kind": "array", "items": () => codec_Tenant};
