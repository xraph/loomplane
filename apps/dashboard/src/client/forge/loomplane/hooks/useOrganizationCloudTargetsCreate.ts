import { mutation } from '@forge-go/client-core';
import { op_organizationCloudTargets_create } from '../ops/organizationCloudTargets.create';
import type { Target } from '../types';

export const useOrganizationCloudTargetsCreate = /*#__PURE__*/ mutation<Target, Target>(op_organizationCloudTargets_create);
