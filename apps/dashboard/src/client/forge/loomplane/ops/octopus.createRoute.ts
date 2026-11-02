import type { OperationMeta } from '../ops';
import { codec_OctopusRouteInputBody } from '../codecs/OctopusRouteInputBody';
import { codec_RouteResult } from '../codecs/RouteResult';

export const op_octopus_createRoute = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/environments/{environmentId}/octopus/{resource}/routes',
  provides: [],
  invalidates: [],
  bodyCodec: codec_OctopusRouteInputBody,
  responseCodec: codec_RouteResult,
} as const satisfies OperationMeta;
