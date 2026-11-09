import { query } from '@forge-go/client-core';
import { op_cloudLocationPolicy_get } from '../ops/cloudLocationPolicy.get';
import type { LocationPolicy } from '../types';

export const useCloudLocationPolicyGet = /*#__PURE__*/ query<LocationPolicy>(op_cloudLocationPolicy_get);
