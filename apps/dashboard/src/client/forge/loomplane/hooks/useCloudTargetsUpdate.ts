import { mutation } from '@forge-go/client-core';
import { op_cloudTargets_update } from '../ops/cloudTargets.update';
import type { Target } from '../types';

export const useCloudTargetsUpdate = /*#__PURE__*/ mutation<Target, Target>(op_cloudTargets_update);
