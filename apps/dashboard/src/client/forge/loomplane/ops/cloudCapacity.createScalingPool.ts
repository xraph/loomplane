import type { OperationMeta } from '../ops';
import { codec_ScalingPool } from '../codecs/ScalingPool';
import { codec_ScalingPoolInputBody } from '../codecs/ScalingPoolInputBody';

export const op_cloudCapacity_createScalingPool = {
  method: 'POST',
  path: '/api/v1/clouds/{cloudId}/scaling-pools',
  rootType: 'ScalingPool',
  provides: [],
  invalidates: ['CapacityPolicyView:{cloudId}', 'CapacityUsage:{cloudId}'],
  bodyCodec: codec_ScalingPoolInputBody,
  responseCodec: codec_ScalingPool,
} as const satisfies OperationMeta;
