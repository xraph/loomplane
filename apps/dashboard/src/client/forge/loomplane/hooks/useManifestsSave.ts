import { mutation } from '@forge-go/client-core';
import { op_manifests_save } from '../ops/manifests.save';
import type { Revision } from '../types';

export const useManifestsSave = /*#__PURE__*/ mutation<Revision, Revision>(op_manifests_save);
