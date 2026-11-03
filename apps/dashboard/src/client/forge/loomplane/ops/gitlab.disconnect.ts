import type { OperationMeta } from '../ops';

export const op_gitlab_disconnect = {
  method: 'DELETE',
  path: '/api/v1/gitlab/connections/{id}',
  entity: 'GitLabConnectionView',
  provides: [],
  invalidates: ['GitLabConnectionView[]'],
} as const satisfies OperationMeta;
