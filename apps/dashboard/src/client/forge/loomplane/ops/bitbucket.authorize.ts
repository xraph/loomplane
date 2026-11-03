import type { OperationMeta } from '../ops';
import { codec_AuthorizeBitbucketInputBody } from '../codecs/AuthorizeBitbucketInputBody';
import { codec_BitbucketAuthorization } from '../codecs/BitbucketAuthorization';

export const op_bitbucket_authorize = {
  method: 'POST',
  path: '/api/v1/bitbucket/authorize',
  provides: [],
  invalidates: [],
  bodyCodec: codec_AuthorizeBitbucketInputBody,
  responseCodec: codec_BitbucketAuthorization,
} as const satisfies OperationMeta;
