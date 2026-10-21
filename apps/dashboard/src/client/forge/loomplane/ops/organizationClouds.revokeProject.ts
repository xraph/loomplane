import type { OperationMeta } from '../ops';
import { codec_ProjectGrant } from '../codecs/ProjectGrant';

export const op_organizationClouds_revokeProject = {
  method: 'DELETE',
  path: '/api/v1/clouds/{cloudId}/projects/{projectId}',
  provides: [],
  invalidates: [],
  responseCodec: codec_ProjectGrant,
} as const satisfies OperationMeta;
