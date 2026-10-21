import { mutation } from '@forge-go/client-core';
import { op_organizationClouds_setDefaultPlacement } from '../ops/organizationClouds.setDefaultPlacement';
import type { Profile } from '../types';

export const useOrganizationCloudsSetDefaultPlacement = /*#__PURE__*/ mutation<Profile, Profile>(op_organizationClouds_setDefaultPlacement);
