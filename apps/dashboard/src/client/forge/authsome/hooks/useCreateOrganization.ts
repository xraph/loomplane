import { mutation } from '@forge-go/client-core';
import { op_createOrganization } from '../ops/createOrganization';
import type { Organization } from '../types';

export const useCreateOrganization = /*#__PURE__*/ mutation<Organization, Organization>(op_createOrganization);
