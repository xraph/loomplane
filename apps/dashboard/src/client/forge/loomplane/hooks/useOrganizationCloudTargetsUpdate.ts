import { mutation } from '@forge-go/client-core';
import { op_organizationCloudTargets_update } from '../ops/organizationCloudTargets.update';
import type { CloudprofilesTarget } from '../types';

export const useOrganizationCloudTargetsUpdate = /*#__PURE__*/ mutation<CloudprofilesTarget, CloudprofilesTarget>(op_organizationCloudTargets_update);
