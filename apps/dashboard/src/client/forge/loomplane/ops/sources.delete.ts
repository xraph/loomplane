import type { OperationMeta } from '../ops';

export const op_sources_delete = {
  method: 'DELETE',
  path: '/api/v1/projects/{id}/sources/{alias}',
  entity: 'DiscoverySource',
  provides: [],
  invalidates: ['DiscoverySource[]'],
} as const satisfies OperationMeta;
