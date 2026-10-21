import { query } from '@forge-go/client-core';
import { op_dnsZones_project } from '../ops/dnsZones.project';
import type { ProjectZoneList } from '../types';

export const useDnsZonesProject = /*#__PURE__*/ query<ProjectZoneList>(op_dnsZones_project);
