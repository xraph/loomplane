import type { OperationMeta } from '../ops';
import { codec_RouteStatus } from '../codecs/RouteStatus';

export const op_domainRoutes_status = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/domain-routes/{routeId}/status',
  provides: [],
  invalidates: [],
  responseCodec: codec_RouteStatus,
} as const satisfies OperationMeta;
