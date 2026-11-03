import type { OperationMeta } from '../ops';
import { codec_AddGitLabHostInputBody } from '../codecs/AddGitLabHostInputBody';
import { codec_GitLabHostView } from '../codecs/GitLabHostView';

export const op_gitlab_addHost = {
  method: 'POST',
  path: '/api/v1/gitlab/hosts',
  entity: 'GitLabHostView',
  rootType: 'GitLabHostView',
  provides: ['GitLabHostView:{id}'],
  invalidates: ['GitLabHostView[]'],
  bodyCodec: codec_AddGitLabHostInputBody,
  responseCodec: codec_GitLabHostView,
} as const satisfies OperationMeta;
