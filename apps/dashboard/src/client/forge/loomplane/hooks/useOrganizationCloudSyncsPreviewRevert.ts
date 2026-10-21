import { mutation } from '@forge-go/client-core';
import { op_organizationCloudSyncs_previewRevert } from '../ops/organizationCloudSyncs.previewRevert';
import type { Preview } from '../types';

export const useOrganizationCloudSyncsPreviewRevert = /*#__PURE__*/ mutation<Preview, Preview>(op_organizationCloudSyncs_previewRevert);
