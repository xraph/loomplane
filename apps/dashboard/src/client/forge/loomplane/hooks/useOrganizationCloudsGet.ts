import { query } from '@forge-go/client-core';
import { op_organizationClouds_get } from '../ops/organizationClouds.get';
import type { Profile } from '../types';

export const useOrganizationCloudsGet = /*#__PURE__*/ query<Profile>(op_organizationClouds_get);
