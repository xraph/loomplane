import type { OperationMeta } from '../ops';
import { codec_HandoffReleaseInputBody } from '../codecs/HandoffReleaseInputBody';
import { codec_RouteOperation } from '../codecs/RouteOperation';

export const op_domainRoutes_releaseHandoff = {
  method: 'POST',
  path: '/api/v1/projects/{projectId}/route-operations/{operationId}/release-handoff',
  entity: 'RouteOperation',
  rootType: 'RouteOperation',
  provides: ['RouteOperation:{id}'],
  invalidates: ['RouteOperation[]'],
  bodyCodec: codec_HandoffReleaseInputBody,
  responseCodec: codec_RouteOperation,
} as const satisfies OperationMeta;
