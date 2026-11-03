import type { OperationMeta } from '../ops';
import { codec_ApplyInputBody } from '../codecs/ApplyInputBody';
import { codec_Node } from '../codecs/Node';

export const op_nodes_drainApply = {
  method: 'POST',
  path: '/api/v1/nodes/{id}/drain/apply',
  entity: 'Node',
  rootType: 'Node',
  provides: ['Node:{id}'],
  invalidates: ['Node[]'],
  bodyCodec: codec_ApplyInputBody,
  responseCodec: codec_Node,
} as const satisfies OperationMeta;
