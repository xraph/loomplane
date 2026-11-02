import { query } from '@forge-go/client-core';
import { op_domainRegistration_search } from '../ops/domainRegistration.search';
import type { DomainOfferList } from '../types';

export const useDomainRegistrationSearch = /*#__PURE__*/ query<DomainOfferList>(op_domainRegistration_search);
