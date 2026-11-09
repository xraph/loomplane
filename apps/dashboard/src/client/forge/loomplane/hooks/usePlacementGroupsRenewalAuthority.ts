import { mutation } from '@forge-go/client-core';
import { op_placementGroups_renewalAuthority } from '../ops/placementGroups.renewalAuthority';
import type { TLSRenewalAuthority } from '../types';

export const usePlacementGroupsRenewalAuthority = /*#__PURE__*/ mutation<TLSRenewalAuthority, TLSRenewalAuthority>(op_placementGroups_renewalAuthority);
