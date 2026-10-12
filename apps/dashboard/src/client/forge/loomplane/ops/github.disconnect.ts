import type { OperationMeta } from '../ops';

export const op_github_disconnect = {
  method: 'DELETE',
  path: '/api/v1/github/installations/{id}',
  entity: 'GitHubInstallation',
  provides: [],
  invalidates: ['GitHubInstallation[]'],
} as const satisfies OperationMeta;
