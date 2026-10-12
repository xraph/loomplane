import type { OperationMeta } from '../ops';
import { codec_ConnectGitHubInputBody } from '../codecs/ConnectGitHubInputBody';
import { codec_GitHubInstallation } from '../codecs/GitHubInstallation';

export const op_github_connect = {
  method: 'POST',
  path: '/api/v1/github/installations',
  entity: 'GitHubInstallation',
  rootType: 'GitHubInstallation',
  provides: ['GitHubInstallation:{id}'],
  invalidates: ['GitHubInstallation[]'],
  bodyCodec: codec_ConnectGitHubInputBody,
  responseCodec: codec_GitHubInstallation,
} as const satisfies OperationMeta;
