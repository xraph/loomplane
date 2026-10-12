import type { OperationMeta } from '../ops';

export const op_sources_delete = {
  method: 'DELETE',
  path: '/api/v1/projects/{id}/sources/{alias}',
  entity: 'Source',
  provides: [],
  invalidates: ['Source[]'],
} as const satisfies OperationMeta;
