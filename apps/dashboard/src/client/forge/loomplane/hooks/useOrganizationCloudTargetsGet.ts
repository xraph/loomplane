import { query } from '@forge-go/client-core';
import { op_organizationCloudTargets_get } from '../ops/organizationCloudTargets.get';
import type { CloudprofilesTarget } from '../types';

export const useOrganizationCloudTargetsGet = /*#__PURE__*/ query<CloudprofilesTarget>(op_organizationCloudTargets_get);
