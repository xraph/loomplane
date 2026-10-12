import type { OperationMeta } from '../ops';
import { codec_ConnectedRepositoryList } from '../codecs/ConnectedRepositoryList';

export const op_github_repositories = {
  method: 'GET',
  path: '/api/v1/github/repositories',
  provides: [],
  invalidates: [],
  responseCodec: codec_ConnectedRepositoryList,
} as const satisfies OperationMeta;
