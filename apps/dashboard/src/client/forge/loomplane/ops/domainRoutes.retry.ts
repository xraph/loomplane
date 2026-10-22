import type { OperationMeta } from '../ops';
import { codec_RouteOperation } from '../codecs/RouteOperation';

export const op_domainRoutes_retry = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/route-operations/{operationId}/retry',
  entity: 'RouteOperation',
  rootType: 'RouteOperation',
  provides: ['RouteOperation:{id}'],
  invalidates: ['RouteOperation[]'],
  responseCodec: codec_RouteOperation,
} as const satisfies OperationMeta;
