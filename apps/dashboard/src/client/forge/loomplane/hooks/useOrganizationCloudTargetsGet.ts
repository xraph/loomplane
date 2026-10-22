import { query } from '@forge-go/client-core';
import { op_organizationCloudTargets_get } from '../ops/organizationCloudTargets.get';
import type { Target } from '../types';

export const useOrganizationCloudTargetsGet = /*#__PURE__*/ query<Target>(op_organizationCloudTargets_get);
