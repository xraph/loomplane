import type { OperationMeta } from '../ops';
import { codec_CloudinfraRoute } from '../codecs/CloudinfraRoute';
import { codec_RouteTTLInputBody } from '../codecs/RouteTTLInputBody';

export const op_domainRoutes_updateTTL = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/domain-routes/{routeId}/ttl',
  entity: 'CloudinfraRoute',
  rootType: 'CloudinfraRoute',
  provides: ['CloudinfraRoute:{id}'],
  invalidates: ['CloudinfraRoute[]'],
  bodyCodec: codec_RouteTTLInputBody,
  responseCodec: codec_CloudinfraRoute,
} as const satisfies OperationMeta;
