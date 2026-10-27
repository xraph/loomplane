import { mutation } from '@forge-go/client-core';
import { op_domainRegistrations_approve } from '../ops/domainRegistrations.approve';
import type { Registration } from '../types';

export const useDomainRegistrationsApprove = /*#__PURE__*/ mutation<Registration, Registration>(op_domainRegistrations_approve);
