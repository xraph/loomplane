import { query } from '@forge-go/client-core';
import { op_manifests_history } from '../ops/manifests.history';
import type { RevisionList } from '../types';

export const useManifestsHistory = /*#__PURE__*/ query<RevisionList>(op_manifests_history);
