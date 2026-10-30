import type { OperationMeta } from '../ops';
import { codec_ManagedRegistryView } from '../codecs/ManagedRegistryView';
import { codec_RegistryActionInputBody } from '../codecs/RegistryActionInputBody';

export const op_registries_action = {
  method: 'POST',
  path: '/api/v1/connections/{connectionId}/registry/actions',
  rootType: 'ManagedRegistryView',
  provides: [],
  invalidates: ['ManagedRegistryView'],
  bodyCodec: codec_RegistryActionInputBody,
  responseCodec: codec_ManagedRegistryView,
} as const satisfies OperationMeta;
