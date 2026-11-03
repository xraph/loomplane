import type { OperationMeta } from '../ops';
import { codec_JoinToken } from '../codecs/JoinToken';
import { codec_JoinTokenRequestBody } from '../codecs/JoinTokenRequestBody';

export const op_nodeJoinTokens_create = {
  method: 'POST',
  path: '/api/v1/nodes/join-tokens',
  provides: [],
  invalidates: [],
  bodyCodec: codec_JoinTokenRequestBody,
  responseCodec: codec_JoinToken,
} as const satisfies OperationMeta;
