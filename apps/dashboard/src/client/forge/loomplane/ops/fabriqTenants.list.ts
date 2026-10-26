import type { OperationMeta } from '../ops';
import { codec_TenantList } from '../codecs/TenantList';

export const op_fabriqTenants_list = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/fabriq/{resource}/tenants',
  entity: 'Tenant',
  rootType: 'TenantList',
  provides: ['Tenant:{id}', 'Tenant[]'],
  invalidates: [],
  responseCodec: codec_TenantList,
} as const satisfies OperationMeta;
