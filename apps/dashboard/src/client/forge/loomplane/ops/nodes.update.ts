import type { OperationMeta } from '../ops';
import { codec_Node } from '../codecs/Node';
import { codec_NodeUpdateBody } from '../codecs/NodeUpdateBody';

export const op_nodes_update = {
  method: 'PATCH',
  path: '/api/v1/nodes/{id}',
  entity: 'Node',
  rootType: 'Node',
  provides: ['Node:{id}'],
  invalidates: ['Node[]'],
  bodyCodec: codec_NodeUpdateBody,
  responseCodec: codec_Node,
} as const satisfies OperationMeta;
