import { query } from '@forge-go/client-core';
import { op_cloudTargets_get } from '../ops/cloudTargets.get';
import type { CloudprofilesTarget } from '../types';

export const useCloudTargetsGet = /*#__PURE__*/ query<CloudprofilesTarget>(op_cloudTargets_get);
