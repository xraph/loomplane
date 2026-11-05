import { query } from '@forge-go/client-core';
import { op_fleetReleases_get } from '../ops/fleetReleases.get';
import type { TemplateRelease } from '../types';

export const useFleetReleasesGet = /*#__PURE__*/ query<TemplateRelease>(op_fleetReleases_get);
