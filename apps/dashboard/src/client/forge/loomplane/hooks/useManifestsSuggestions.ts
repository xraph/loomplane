import { query } from '@forge-go/client-core';
import { op_manifests_suggestions } from '../ops/manifests.suggestions';
import type { ServiceSuggestions } from '../types';

export const useManifestsSuggestions = /*#__PURE__*/ query<ServiceSuggestions>(op_manifests_suggestions);
