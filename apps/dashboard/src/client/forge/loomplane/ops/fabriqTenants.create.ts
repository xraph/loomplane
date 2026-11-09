import type { OperationMeta } from '../ops';
import { codec_FabriqloomTenant } from '../codecs/FabriqloomTenant';
import { codec_PlanningTenantInputBody } from '../codecs/PlanningTenantInputBody';

export const op_fabriqTenants_create = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/fabriq/{resource}/tenants',
  entity: 'FabriqloomTenant',
  rootType: 'FabriqloomTenant',
  provides: ['FabriqloomTenant:{id}'],
  invalidates: ['FabriqloomTenant[]'],
  bodyCodec: codec_PlanningTenantInputBody,
  responseCodec: codec_FabriqloomTenant,
} as const satisfies OperationMeta;
