import type { OperationMeta } from '../ops';

export const op_environments_delete = {
  method: 'DELETE',
  path: '/api/v1/projects/{id}/environments/{envId}',
  entity: 'Environment',
  provides: [],
  invalidates: ['Environment[]'],
} as const satisfies OperationMeta;
