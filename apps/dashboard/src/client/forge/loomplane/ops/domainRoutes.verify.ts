import type { OperationMeta } from '../ops';
import { codec_RouteOperation } from '../codecs/RouteOperation';

export const op_domainRoutes_verify = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/domain-routes/{routeId}/verify',
  entity: 'RouteOperation',
  rootType: 'RouteOperation',
  provides: ['RouteOperation:{id}'],
  invalidates: ['RouteOperation[]'],
  responseCodec: codec_RouteOperation,
} as const satisfies OperationMeta;
