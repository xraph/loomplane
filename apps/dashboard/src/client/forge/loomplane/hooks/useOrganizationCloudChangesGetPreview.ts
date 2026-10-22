import { query } from '@forge-go/client-core';
import { op_organizationCloudChanges_getPreview } from '../ops/organizationCloudChanges.getPreview';
import type { Preview } from '../types';

export const useOrganizationCloudChangesGetPreview = /*#__PURE__*/ query<Preview>(op_organizationCloudChanges_getPreview);
