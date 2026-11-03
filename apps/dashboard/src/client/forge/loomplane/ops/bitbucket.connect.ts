import type { OperationMeta } from '../ops';
import { codec_BitbucketConnectInputBody } from '../codecs/BitbucketConnectInputBody';
import { codec_BitbucketWorkspaceView } from '../codecs/BitbucketWorkspaceView';

export const op_bitbucket_connect = {
  method: 'POST',
  path: '/api/v1/bitbucket/workspaces',
  entity: 'BitbucketWorkspaceView',
  rootType: 'BitbucketWorkspaceView',
  provides: ['BitbucketWorkspaceView:{id}'],
  invalidates: ['BitbucketWorkspaceView[]'],
  bodyCodec: codec_BitbucketConnectInputBody,
  responseCodec: codec_BitbucketWorkspaceView,
} as const satisfies OperationMeta;
