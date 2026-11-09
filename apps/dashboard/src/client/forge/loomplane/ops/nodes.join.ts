import type { OperationMeta } from '../ops';
import { codec_NodeJoinBody } from '../codecs/NodeJoinBody';
import { codec_NodeJoined } from '../codecs/NodeJoined';

export const op_nodes_join = {
  method: 'POST',
  path: '/api/v1/nodes/join',
  provides: [],
  invalidates: [],
  bodyCodec: codec_NodeJoinBody,
  responseCodec: codec_NodeJoined,
} as const satisfies OperationMeta;
