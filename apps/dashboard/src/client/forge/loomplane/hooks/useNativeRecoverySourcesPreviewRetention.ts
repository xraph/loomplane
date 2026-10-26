import { query } from '@forge-go/client-core';
import { op_nativeRecoverySources_previewRetention } from '../ops/nativeRecoverySources.previewRetention';
import type { RetentionPreview } from '../types';

export const useNativeRecoverySourcesPreviewRetention = /*#__PURE__*/ query<RetentionPreview>(op_nativeRecoverySources_previewRetention);
