import type { OperationMeta } from '../ops';

export const op_teams_delete = {
  method: 'DELETE',
  path: '/api/v1/teams/{id}',
  provides: [],
  invalidates: ['Team[]'],
} as const satisfies OperationMeta;
