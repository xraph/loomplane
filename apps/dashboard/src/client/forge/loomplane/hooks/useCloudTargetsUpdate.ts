import { mutation } from '@forge-go/client-core';
import { op_cloudTargets_update } from '../ops/cloudTargets.update';
import type { CloudprofilesTarget } from '../types';

export const useCloudTargetsUpdate = /*#__PURE__*/ mutation<CloudprofilesTarget, CloudprofilesTarget>(op_cloudTargets_update);
