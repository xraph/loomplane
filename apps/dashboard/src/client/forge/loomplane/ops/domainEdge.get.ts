import type { OperationMeta } from '../ops';
import { codec_EdgeState } from '../codecs/EdgeState';

export const op_domainEdge_get = {
  method: 'GET',
  path: '/api/v1/projects/{projectId}/domain-routes/{routeId}/edge',
  provides: [],
  invalidates: [],
  responseCodec: codec_EdgeState,
} as const satisfies OperationMeta;
