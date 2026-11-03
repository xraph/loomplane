import type { OperationMeta } from '../ops';
import { codec_BitbucketStatus } from '../codecs/BitbucketStatus';

export const op_bitbucket_status = {
  method: 'GET',
  path: '/api/v1/bitbucket',
  entity: 'BitbucketWorkspaceView',
  rootType: 'BitbucketStatus',
  provides: ['BitbucketWorkspaceView:{id}', 'BitbucketWorkspaceView[]'],
  invalidates: [],
  responseCodec: codec_BitbucketStatus,
} as const satisfies OperationMeta;
