import type { OperationMeta } from '../ops';
import { codec_ApplyInputBody } from '../codecs/ApplyInputBody';
import { codec_RouteOperation } from '../codecs/RouteOperation';

export const op_domainRoutes_apply = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/route-operations/{operationId}/apply',
  entity: 'RouteOperation',
  rootType: 'RouteOperation',
  provides: ['RouteOperation:{id}'],
  invalidates: ['RouteOperation[]'],
  bodyCodec: codec_ApplyInputBody,
  responseCodec: codec_RouteOperation,
} as const satisfies OperationMeta;
