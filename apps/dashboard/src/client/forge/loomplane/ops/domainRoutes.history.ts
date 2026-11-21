import type { OperationMeta } from '../ops';
import { codec_DomainOperationList } from '../codecs/DomainOperationList';

export const op_domainRoutes_history = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/domain-routes/{routeId}/operations',
  entity: 'RouteOperation',
  rootType: 'DomainOperationList',
  provides: ['RouteOperation:{id}', 'RouteOperation[]'],
  invalidates: [],
  responseCodec: codec_DomainOperationList,
} as const satisfies OperationMeta;
