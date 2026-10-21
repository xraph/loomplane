import { mutation } from '@forge-go/client-core';
import { op_cloudSyncs_retry } from '../ops/cloudSyncs.retry';
import type { Sync } from '../types';

export const useCloudSyncsRetry = /*#__PURE__*/ mutation<Sync, Sync>(op_cloudSyncs_retry);
