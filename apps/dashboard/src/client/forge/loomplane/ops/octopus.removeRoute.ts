import type { OperationMeta } from '../ops';
import { codec_RouteRevisionInputBody } from '../codecs/RouteRevisionInputBody';
import { codec_SyncResult } from '../codecs/SyncResult';

export const op_octopus_removeRoute = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/octopus/{resource}/routes/{routeId}/remove',
  provides: [],
  invalidates: [],
  bodyCodec: codec_RouteRevisionInputBody,
  responseCodec: codec_SyncResult,
} as const satisfies OperationMeta;
