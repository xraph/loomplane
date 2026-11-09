import { mutation } from '@forge-go/client-core';
import { op_placementGroups_destinationMapping } from '../ops/placementGroups.destinationMapping';
import type { GroupVerificationResult } from '../types';

export const usePlacementGroupsDestinationMapping = /*#__PURE__*/ mutation<GroupVerificationResult, GroupVerificationResult>(op_placementGroups_destinationMapping);
