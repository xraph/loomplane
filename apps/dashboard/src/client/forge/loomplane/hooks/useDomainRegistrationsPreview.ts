import { mutation } from '@forge-go/client-core';
import { op_domainRegistrations_preview } from '../ops/domainRegistrations.preview';
import type { Registration } from '../types';

export const useDomainRegistrationsPreview = /*#__PURE__*/ mutation<Registration, Registration>(op_domainRegistrations_preview);
