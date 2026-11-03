import type { OperationMeta } from '../ops';

export const op_gitlab_removeHost = {
  method: 'DELETE',
  path: '/api/v1/gitlab/hosts/{id}',
  entity: 'GitLabHostView',
  provides: [],
  invalidates: ['GitLabHostView[]'],
} as const satisfies OperationMeta;
