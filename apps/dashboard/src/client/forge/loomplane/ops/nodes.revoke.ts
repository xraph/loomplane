import type { OperationMeta } from '../ops';
import { codec_Node } from '../codecs/Node';

export const op_nodes_revoke = {
  method: 'POST',
  path: '/api/v1/nodes/{id}/revoke',
  entity: 'Node',
  rootType: 'Node',
  provides: ['Node:{id}'],
  invalidates: ['Node[]'],
  responseCodec: codec_Node,
} as const satisfies OperationMeta;
