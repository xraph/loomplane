import { mutation } from '@forge-go/client-core';
import { op_cloudTargets_create } from '../ops/cloudTargets.create';
import type { CloudprofilesTarget } from '../types';

export const useCloudTargetsCreate = /*#__PURE__*/ mutation<CloudprofilesTarget, CloudprofilesTarget>(op_cloudTargets_create);
