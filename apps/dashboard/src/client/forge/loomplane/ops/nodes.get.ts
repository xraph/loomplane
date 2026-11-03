import type { OperationMeta } from '../ops';
import { codec_Node } from '../codecs/Node';

export const op_nodes_get = {
  method: 'GET',
  path: '/api/v1/nodes/{id}',
  entity: 'Node',
  rootType: 'Node',
  provides: ['Node:{id}'],
  invalidates: [],
  responseCodec: codec_Node,
} as const satisfies OperationMeta;
