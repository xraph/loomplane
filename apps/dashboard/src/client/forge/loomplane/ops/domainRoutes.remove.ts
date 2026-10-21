import type { OperationMeta } from '../ops';
import { codec_RemoveRouteInputBody } from '../codecs/RemoveRouteInputBody';
import { codec_RouteOperation } from '../codecs/RouteOperation';

export const op_domainRoutes_remove = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/domain-routes/{routeId}/remove',
  entity: 'RouteOperation',
  rootType: 'RouteOperation',
  provides: ['RouteOperation:{id}'],
  invalidates: ['RouteOperation[]'],
  bodyCodec: codec_RemoveRouteInputBody,
  responseCodec: codec_RouteOperation,
} as const satisfies OperationMeta;
