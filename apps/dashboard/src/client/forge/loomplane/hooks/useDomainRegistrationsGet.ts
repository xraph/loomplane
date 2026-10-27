import { query } from '@forge-go/client-core';
import { op_domainRegistrations_get } from '../ops/domainRegistrations.get';
import type { Registration } from '../types';

export const useDomainRegistrationsGet = /*#__PURE__*/ query<Registration>(op_domainRegistrations_get);
