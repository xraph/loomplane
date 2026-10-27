import { query } from '@forge-go/client-core';
import { op_gpuTargets_list } from '../ops/gpuTargets.list';
import type { GPUTargetList } from '../types';

export const useGpuTargetsList = /*#__PURE__*/ query<GPUTargetList>(op_gpuTargets_list);
