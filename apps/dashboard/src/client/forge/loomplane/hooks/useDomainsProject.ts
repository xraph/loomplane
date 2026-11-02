import { query } from '@forge-go/client-core';
import { op_domains_project } from '../ops/domains.project';
import type { ProjectDomainList } from '../types';

export const useDomainsProject = /*#__PURE__*/ query<ProjectDomainList>(op_domains_project);
