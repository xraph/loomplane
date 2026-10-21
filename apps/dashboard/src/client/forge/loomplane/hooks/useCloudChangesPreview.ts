import { mutation } from '@forge-go/client-core';
import { op_cloudChanges_preview } from '../ops/cloudChanges.preview';
import type { Preview } from '../types';

export const useCloudChangesPreview = /*#__PURE__*/ mutation<Preview, Preview>(op_cloudChanges_preview);
