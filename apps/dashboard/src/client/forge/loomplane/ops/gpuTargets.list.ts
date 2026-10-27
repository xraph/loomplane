import type { OperationMeta } from '../ops';
import { codec_GPUTargetList } from '../codecs/GPUTargetList';

export const op_gpuTargets_list = {
  method: 'GET',
  path: '/api/v1/gpu-targets',
  provides: [],
  invalidates: [],
  responseCodec: codec_GPUTargetList,
} as const satisfies OperationMeta;
