import { query } from '@forge-go/client-core';
import { op_domains_overview } from '../ops/domains.overview';
import type { DomainOverview } from '../types';

export const useDomainsOverview = /*#__PURE__*/ query<DomainOverview>(op_domains_overview);
