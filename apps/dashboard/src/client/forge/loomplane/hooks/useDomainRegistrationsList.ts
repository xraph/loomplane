import { query } from '@forge-go/client-core';
import { op_domainRegistrations_list } from '../ops/domainRegistrations.list';
import type { RegistrationList } from '../types';

export const useDomainRegistrationsList = /*#__PURE__*/ query<RegistrationList>(op_domainRegistrations_list);
