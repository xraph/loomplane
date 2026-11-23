import type { OperationMeta } from '../ops';
import { codec_RunnersJoinToken } from '../codecs/RunnersJoinToken';
import { codec_RunnersJoinTokenRequestBody } from '../codecs/RunnersJoinTokenRequestBody';

export const op_runnerPools_joinToken = {
  method: 'POST',
  path: '/api/v1/runner-pools/{id}/join-tokens',
  provides: [],
  invalidates: [],
  bodyCodec: codec_RunnersJoinTokenRequestBody,
  responseCodec: codec_RunnersJoinToken,
} as const satisfies OperationMeta;
