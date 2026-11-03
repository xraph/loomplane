import type { OperationMeta } from '../ops';

export const op_bitbucket_disconnect = {
  method: 'DELETE',
  path: '/api/v1/bitbucket/workspaces/{id}',
  entity: 'BitbucketWorkspace',
  provides: [],
  invalidates: ['BitbucketWorkspace[]'],
} as const satisfies OperationMeta;
