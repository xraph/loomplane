import { mutation } from '@forge-go/client-core';
import { op_organizationCloudSyncs_completeRouting } from '../ops/organizationCloudSyncs.completeRouting';
import type { Sync } from '../types';

export const useOrganizationCloudSyncsCompleteRouting = /*#__PURE__*/ mutation<Sync, Sync>(op_organizationCloudSyncs_completeRouting);
