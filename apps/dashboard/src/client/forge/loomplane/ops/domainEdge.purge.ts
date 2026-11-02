import type { OperationMeta } from '../ops';
import { codec_EdgePurgeInputBody } from '../codecs/EdgePurgeInputBody';
import { codec_EdgeState } from '../codecs/EdgeState';

export const op_domainEdge_purge = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/domain-routes/{routeId}/edge/purge',
  provides: [],
  invalidates: [],
  bodyCodec: codec_EdgePurgeInputBody,
  responseCodec: codec_EdgeState,
} as const satisfies OperationMeta;
