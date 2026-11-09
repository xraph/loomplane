import type { OperationMeta } from '../ops';
import { codec_EdgeInputBody } from '../codecs/EdgeInputBody';
import { codec_EdgeState } from '../codecs/EdgeState';

export const op_domainEdge_update = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/domain-routes/{routeId}/edge',
  provides: [],
  invalidates: [],
  bodyCodec: codec_EdgeInputBody,
  responseCodec: codec_EdgeState,
} as const satisfies OperationMeta;
