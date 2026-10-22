import { mutation } from '@forge-go/client-core';
import { op_organizationCloudSyncs_applyRevert } from '../ops/organizationCloudSyncs.applyRevert';
import type { Sync } from '../types';

export const useOrganizationCloudSyncsApplyRevert = /*#__PURE__*/ mutation<Sync, Sync>(op_organizationCloudSyncs_applyRevert);
