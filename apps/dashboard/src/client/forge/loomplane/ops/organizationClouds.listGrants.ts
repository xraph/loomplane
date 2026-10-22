import type { OperationMeta } from '../ops';
import { codec_CloudProjectGrantList } from '../codecs/CloudProjectGrantList';

export const op_organizationClouds_listGrants = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/projects',
  provides: [],
  invalidates: [],
  responseCodec: codec_CloudProjectGrantList,
} as const satisfies OperationMeta;
