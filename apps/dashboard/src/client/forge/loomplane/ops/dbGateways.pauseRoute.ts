import type { OperationMeta } from '../ops';
import { codec_RouteSummary } from '../codecs/RouteSummary';

export const op_dbGateways_pauseRoute = {
  method: 'POST',
  path: '/api/v1/projects/{id}/db-gateways/{gatewayId}/routes/{routeId}/pause',
  entity: 'RouteSummary',
  rootType: 'RouteSummary',
  provides: ['RouteSummary:{id}'],
  invalidates: ['RouteSummary[]'],
  responseCodec: codec_RouteSummary,
} as const satisfies OperationMeta;
