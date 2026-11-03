import type { OperationMeta } from '../ops';
import { codec_ConnectGitLabInputBody } from '../codecs/ConnectGitLabInputBody';
import { codec_GitLabConnectionView } from '../codecs/GitLabConnectionView';

export const op_gitlab_connect = {
  method: 'POST',
  path: '/api/v1/gitlab/connections',
  entity: 'GitLabConnectionView',
  rootType: 'GitLabConnectionView',
  provides: ['GitLabConnectionView:{id}'],
  invalidates: ['GitLabConnectionView[]'],
  bodyCodec: codec_ConnectGitLabInputBody,
  responseCodec: codec_GitLabConnectionView,
} as const satisfies OperationMeta;
