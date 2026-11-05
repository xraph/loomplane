import { mutation } from '@forge-go/client-core';
import { op_placementGroups_timeoutVerification } from '../ops/placementGroups.timeoutVerification';
import type { GroupVerificationResult } from '../types';

export const usePlacementGroupsTimeoutVerification = /*#__PURE__*/ mutation<GroupVerificationResult, GroupVerificationResult>(op_placementGroups_timeoutVerification);
