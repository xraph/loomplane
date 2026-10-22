import { query } from '@forge-go/client-core';
import { op_organizationCloudSyncs_get } from '../ops/organizationCloudSyncs.get';
import type { Sync } from '../types';

export const useOrganizationCloudSyncsGet = /*#__PURE__*/ query<Sync>(op_organizationCloudSyncs_get);
