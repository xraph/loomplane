import type { OperationMeta } from '../ops';
import { codec_RouteInputBody } from '../codecs/RouteInputBody';
import { codec_RouteOperation } from '../codecs/RouteOperation';

export const op_domainRoutes_preview = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/domain-routes/preview',
  entity: 'RouteOperation',
  rootType: 'RouteOperation',
  provides: ['RouteOperation:{id}'],
  invalidates: ['RouteOperation[]'],
  bodyCodec: codec_RouteInputBody,
  responseCodec: codec_RouteOperation,
} as const satisfies OperationMeta;
