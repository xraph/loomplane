import type { OperationMeta } from '../ops';
import { codec_DomainRouteList } from '../codecs/DomainRouteList';

export const op_domainRoutes_list = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/domain-routes',
  provides: [],
  invalidates: [],
  responseCodec: codec_DomainRouteList,
} as const satisfies OperationMeta;
