import type { Codec } from '../codec-runtime';
import { codec_FabriqloomTenant } from './FabriqloomTenant';

export const codec_PlanningTenantList_tenants: Codec = {"kind": "array", "items": () => codec_FabriqloomTenant};
