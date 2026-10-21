import { mutation } from '@forge-go/client-core';
import { op_organizationCloudSyncs_retry } from '../ops/organizationCloudSyncs.retry';
import type { Sync } from '../types';

export const useOrganizationCloudSyncsRetry = /*#__PURE__*/ mutation<Sync, Sync>(op_organizationCloudSyncs_retry);
