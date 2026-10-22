import type { OperationMeta } from '../ops';
import { codec_CloudProjectGrantInputBody } from '../codecs/CloudProjectGrantInputBody';
import { codec_ProjectGrant } from '../codecs/ProjectGrant';

export const op_organizationClouds_grantProject = {
  method: 'PUT',
  path: '/api/v1/clouds/{cloudId}/projects/{projectId}',
  provides: [],
  invalidates: [],
  bodyCodec: codec_CloudProjectGrantInputBody,
  responseCodec: codec_ProjectGrant,
} as const satisfies OperationMeta;
