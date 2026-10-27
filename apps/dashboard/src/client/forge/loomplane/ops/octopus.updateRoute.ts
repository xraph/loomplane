import type { OperationMeta } from '../ops';
import { codec_OctopusRouteInputBody } from '../codecs/OctopusRouteInputBody';
import { codec_RouteResult } from '../codecs/RouteResult';

export const op_octopus_updateRoute = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/octopus/{resource}/routes/{routeId}/update',
  provides: [],
  invalidates: [],
  bodyCodec: codec_OctopusRouteInputBody,
  responseCodec: codec_RouteResult,
} as const satisfies OperationMeta;
