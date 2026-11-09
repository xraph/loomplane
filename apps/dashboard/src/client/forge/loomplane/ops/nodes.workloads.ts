import type { OperationMeta } from '../ops';
import { codec_NodeWorkloadList } from '../codecs/NodeWorkloadList';

export const op_nodes_workloads = {
  method: 'GET',
  path: '/api/v1/nodes/{id}/workloads',
  provides: [],
  invalidates: [],
  responseCodec: codec_NodeWorkloadList,
} as const satisfies OperationMeta;
