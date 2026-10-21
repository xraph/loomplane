import { mutation } from '@forge-go/client-core';
import { op_cloudChanges_apply } from '../ops/cloudChanges.apply';
import type { Sync } from '../types';

export const useCloudChangesApply = /*#__PURE__*/ mutation<Sync, Sync>(op_cloudChanges_apply);
