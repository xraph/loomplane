import type { OperationMeta } from '../ops';
import { codec_NodeList } from '../codecs/NodeList';

export const op_nodes_list = {
  method: 'GET',
  path: '/api/v1/nodes',
  entity: 'Node',
  rootType: 'NodeList',
  provides: ['Node:{id}', 'Node[]'],
  invalidates: [],
  responseCodec: codec_NodeList,
} as const satisfies OperationMeta;
