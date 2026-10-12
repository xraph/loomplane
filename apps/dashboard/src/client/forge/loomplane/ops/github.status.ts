import type { OperationMeta } from '../ops';
import { codec_GitHubStatus } from '../codecs/GitHubStatus';

export const op_github_status = {
  method: 'GET',
  path: '/api/v1/github',
  entity: 'GitHubInstallation',
  rootType: 'GitHubStatus',
  provides: ['GitHubInstallation:{id}', 'GitHubInstallation[]'],
  invalidates: [],
  responseCodec: codec_GitHubStatus,
} as const satisfies OperationMeta;
