import { mutation } from '@forge-go/client-core';
import { op_organizationClouds_update } from '../ops/organizationClouds.update';
import type { Profile } from '../types';

export const useOrganizationCloudsUpdate = /*#__PURE__*/ mutation<Profile, Profile>(op_organizationClouds_update);
