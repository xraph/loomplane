import type { OperationMeta } from '../ops';
import { codec_CloudinfraRouteStatus } from '../codecs/CloudinfraRouteStatus';

export const op_domainRoutes_status = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/domain-routes/{routeId}/status',
  provides: [],
  invalidates: [],
  responseCodec: codec_CloudinfraRouteStatus,
} as const satisfies OperationMeta;
