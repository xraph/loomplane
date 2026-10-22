import { mutation } from '@forge-go/client-core';
import { op_organizationCloudTargets_update } from '../ops/organizationCloudTargets.update';
import type { Target } from '../types';

export const useOrganizationCloudTargetsUpdate = /*#__PURE__*/ mutation<Target, Target>(op_organizationCloudTargets_update);
