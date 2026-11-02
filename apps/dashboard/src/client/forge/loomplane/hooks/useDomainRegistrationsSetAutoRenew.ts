import { mutation } from '@forge-go/client-core';
import { op_domainRegistrations_setAutoRenew } from '../ops/domainRegistrations.setAutoRenew';
import type { Registration } from '../types';

export const useDomainRegistrationsSetAutoRenew = /*#__PURE__*/ mutation<Registration, Registration>(op_domainRegistrations_setAutoRenew);
