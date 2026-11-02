import { query } from '@forge-go/client-core';
import { op_domainRegistration_extensions } from '../ops/domainRegistration.extensions';
import type { RegistrationExtensionList } from '../types';

export const useDomainRegistrationExtensions = /*#__PURE__*/ query<RegistrationExtensionList>(op_domainRegistration_extensions);
