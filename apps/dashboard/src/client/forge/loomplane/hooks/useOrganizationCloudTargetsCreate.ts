import { mutation } from '@forge-go/client-core';
import { op_organizationCloudTargets_create } from '../ops/organizationCloudTargets.create';
import type { CloudprofilesTarget } from '../types';

export const useOrganizationCloudTargetsCreate = /*#__PURE__*/ mutation<CloudprofilesTarget, CloudprofilesTarget>(op_organizationCloudTargets_create);
