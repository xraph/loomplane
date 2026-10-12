import type { OperationMeta } from '../ops';

export const op_projects_delete = {
  method: 'DELETE',
  path: '/api/v1/projects/{id}',
  entity: 'Project',
  provides: [],
  invalidates: ['Project[]'],
} as const satisfies OperationMeta;
