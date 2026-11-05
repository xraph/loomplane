import type { OperationMeta } from '../ops';
import { codec_PlanningTenantList } from '../codecs/PlanningTenantList';

export const op_fabriqTenants_list = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/fabriq/{resource}/tenants',
  entity: 'FabriqloomTenant',
  rootType: 'PlanningTenantList',
  provides: ['FabriqloomTenant:{id}', 'FabriqloomTenant[]'],
  invalidates: [],
  responseCodec: codec_PlanningTenantList,
} as const satisfies OperationMeta;
