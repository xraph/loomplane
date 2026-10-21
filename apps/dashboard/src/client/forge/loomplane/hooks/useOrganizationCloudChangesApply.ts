import { mutation } from '@forge-go/client-core';
import { op_organizationCloudChanges_apply } from '../ops/organizationCloudChanges.apply';
import type { Sync } from '../types';

export const useOrganizationCloudChangesApply = /*#__PURE__*/ mutation<Sync, Sync>(op_organizationCloudChanges_apply);
