import { mutation } from '@forge-go/client-core';
import { op_placementGroups_prerequisite } from '../ops/placementGroups.prerequisite';
import type { MemberPrerequisite } from '../types';

export const usePlacementGroupsPrerequisite = /*#__PURE__*/ mutation<MemberPrerequisite, MemberPrerequisite>(op_placementGroups_prerequisite);
