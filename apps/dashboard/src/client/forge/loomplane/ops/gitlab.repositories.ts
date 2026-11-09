import type { OperationMeta } from '../ops';
import { codec_GitLabRepositoryList } from '../codecs/GitLabRepositoryList';

export const op_gitlab_repositories = {
  method: 'GET',
  path: '/api/v1/gitlab/repositories',
  provides: [],
  invalidates: [],
  responseCodec: codec_GitLabRepositoryList,
} as const satisfies OperationMeta;
