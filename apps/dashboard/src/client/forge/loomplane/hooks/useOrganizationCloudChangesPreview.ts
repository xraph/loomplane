import { mutation } from '@forge-go/client-core';
import { op_organizationCloudChanges_preview } from '../ops/organizationCloudChanges.preview';
import type { Preview } from '../types';

export const useOrganizationCloudChangesPreview = /*#__PURE__*/ mutation<Preview, Preview>(op_organizationCloudChanges_preview);
