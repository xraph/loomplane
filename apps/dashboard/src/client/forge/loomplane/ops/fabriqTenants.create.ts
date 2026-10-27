import type { OperationMeta } from '../ops';
import { codec_Tenant } from '../codecs/Tenant';
import { codec_TenantInputBody } from '../codecs/TenantInputBody';

export const op_fabriqTenants_create = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/fabriq/{resource}/tenants',
  entity: 'Tenant',
  rootType: 'Tenant',
  provides: ['Tenant:{id}'],
  invalidates: ['Tenant[]'],
  bodyCodec: codec_TenantInputBody,
  responseCodec: codec_Tenant,
} as const satisfies OperationMeta;
