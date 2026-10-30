import type { OperationMeta } from '../ops';
import { codec_EdgeState } from '../codecs/EdgeState';
import { codec_RemoveRouteInputBody } from '../codecs/RemoveRouteInputBody';

export const op_domainEdge_retry = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/domain-routes/{routeId}/edge/retry',
  provides: [],
  invalidates: [],
  bodyCodec: codec_RemoveRouteInputBody,
  responseCodec: codec_EdgeState,
} as const satisfies OperationMeta;
