import { query } from '@forge-go/client-core';
import { op_cloudTargets_get } from '../ops/cloudTargets.get';
import type { Target } from '../types';

export const useCloudTargetsGet = /*#__PURE__*/ query<Target>(op_cloudTargets_get);
