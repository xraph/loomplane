import { mutation } from '@forge-go/client-core';
import { op_cloudSyncs_completeRouting } from '../ops/cloudSyncs.completeRouting';
import type { Sync } from '../types';

export const useCloudSyncsCompleteRouting = /*#__PURE__*/ mutation<Sync, Sync>(op_cloudSyncs_completeRouting);
