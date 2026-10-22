import type { OperationMeta } from '../ops';
import { codec_HandoffInputBody } from '../codecs/HandoffInputBody';
import { codec_RouteOperation } from '../codecs/RouteOperation';

export const op_domainRoutes_prepareHandoff = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/domain-routes/{routeId}/prepare-handoff',
  entity: 'RouteOperation',
  rootType: 'RouteOperation',
  provides: ['RouteOperation:{id}'],
  invalidates: ['RouteOperation[]'],
  bodyCodec: codec_HandoffInputBody,
  responseCodec: codec_RouteOperation,
} as const satisfies OperationMeta;
