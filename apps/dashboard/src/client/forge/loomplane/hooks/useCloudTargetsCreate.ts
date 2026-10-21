import { mutation } from '@forge-go/client-core';
import { op_cloudTargets_create } from '../ops/cloudTargets.create';
import type { Target } from '../types';

export const useCloudTargetsCreate = /*#__PURE__*/ mutation<Target, Target>(op_cloudTargets_create);
