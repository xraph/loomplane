import type { Codec } from '../codec-runtime';
import { codec_PlanningTenantList_tenants } from './PlanningTenantList.tenants';

export const codec_PlanningTenantList: Codec = {"kind": "object", "fields": {"tenants": {"ts": "tenants", "codec": () => codec_PlanningTenantList_tenants}}, "required": ["tenants"]};
