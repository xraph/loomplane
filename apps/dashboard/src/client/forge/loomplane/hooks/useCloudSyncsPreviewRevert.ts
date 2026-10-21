import { mutation } from '@forge-go/client-core';
import { op_cloudSyncs_previewRevert } from '../ops/cloudSyncs.previewRevert';
import type { Preview } from '../types';

export const useCloudSyncsPreviewRevert = /*#__PURE__*/ mutation<Preview, Preview>(op_cloudSyncs_previewRevert);
