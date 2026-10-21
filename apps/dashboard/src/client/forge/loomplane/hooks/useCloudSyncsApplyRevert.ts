import { mutation } from '@forge-go/client-core';
import { op_cloudSyncs_applyRevert } from '../ops/cloudSyncs.applyRevert';
import type { Sync } from '../types';

export const useCloudSyncsApplyRevert = /*#__PURE__*/ mutation<Sync, Sync>(op_cloudSyncs_applyRevert);
