import type { OperationMeta } from '../ops';

export const op_bitbucket_disconnect = {
  method: 'DELETE',
  path: '/api/v1/bitbucket/workspaces/{id}',
  entity: 'BitbucketWorkspaceView',
  provides: [],
  invalidates: ['BitbucketWorkspaceView[]'],
} as const satisfies OperationMeta;
