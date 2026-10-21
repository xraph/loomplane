import { query } from '@forge-go/client-core';
import { op_cloudChanges_getPreview } from '../ops/cloudChanges.getPreview';
import type { Preview } from '../types';

export const useCloudChangesGetPreview = /*#__PURE__*/ query<Preview>(op_cloudChanges_getPreview);
