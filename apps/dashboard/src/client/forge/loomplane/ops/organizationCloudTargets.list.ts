import type { OperationMeta } from '../ops';
import { codec_CloudTargetList } from '../codecs/CloudTargetList';

export const op_organizationCloudTargets_list = {
  method: 'GET',
  path: '/api/v1/clouds/{cloudId}/targets',
  provides: [],
  invalidates: [],
  responseCodec: codec_CloudTargetList,
} as const satisfies OperationMeta;
