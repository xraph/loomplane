import { mutation } from '@forge-go/client-core';
import { op_organizationClouds_create } from '../ops/organizationClouds.create';
import type { Profile } from '../types';

export const useOrganizationCloudsCreate = /*#__PURE__*/ mutation<Profile, Profile>(op_organizationClouds_create);
