import type { OperationMeta } from '../ops';
import { codec_GatewayList } from '../codecs/GatewayList';

export const op_dbGateways_list = {
  method: 'GET',
  path: '/api/v1/projects/{id}/db-gateways',
  entity: 'GatewayStatus',
  rootType: 'GatewayList',
  provides: ['GatewayStatus:{id}', 'GatewayStatus[]'],
  invalidates: [],
  responseCodec: codec_GatewayList,
} as const satisfies OperationMeta;
