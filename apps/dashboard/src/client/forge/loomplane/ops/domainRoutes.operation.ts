import type { OperationMeta } from '../ops';
import { codec_RouteOperation } from '../codecs/RouteOperation';

export const op_domainRoutes_operation = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/route-operations/{operationId}',
  entity: 'RouteOperation',
  rootType: 'RouteOperation',
  provides: ['RouteOperation:{id}'],
  invalidates: [],
  responseCodec: codec_RouteOperation,
} as const satisfies OperationMeta;
