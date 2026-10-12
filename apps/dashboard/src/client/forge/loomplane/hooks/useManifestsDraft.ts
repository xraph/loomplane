import { query } from '@forge-go/client-core';
import { op_manifests_draft } from '../ops/manifests.draft';
import type { ManifestDraft } from '../types';

export const useManifestsDraft = /*#__PURE__*/ query<ManifestDraft>(op_manifests_draft);
