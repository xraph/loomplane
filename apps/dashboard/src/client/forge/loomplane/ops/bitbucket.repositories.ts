import type { OperationMeta } from '../ops';
import { codec_BitbucketRepositoryList } from '../codecs/BitbucketRepositoryList';

export const op_bitbucket_repositories = {
  method: 'GET',
  path: '/api/v1/bitbucket/repositories',
  provides: [],
  invalidates: [],
  responseCodec: codec_BitbucketRepositoryList,
} as const satisfies OperationMeta;
