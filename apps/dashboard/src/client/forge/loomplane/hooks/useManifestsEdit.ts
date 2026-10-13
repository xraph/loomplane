import { mutation } from '@forge-go/client-core';
import { op_manifests_edit } from '../ops/manifests.edit';
import type { Revision } from '../types';

export const useManifestsEdit = /*#__PURE__*/ mutation<Revision, Revision>(op_manifests_edit);
