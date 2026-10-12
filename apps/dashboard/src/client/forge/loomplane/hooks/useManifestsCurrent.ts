import { query } from '@forge-go/client-core';
import { op_manifests_current } from '../ops/manifests.current';
import type { Revision } from '../types';

export const useManifestsCurrent = /*#__PURE__*/ query<Revision>(op_manifests_current);
