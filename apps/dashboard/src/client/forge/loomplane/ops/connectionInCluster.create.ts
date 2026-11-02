import type { OperationMeta } from '../ops';
import { codec_Connection } from '../codecs/Connection';
import { codec_InClusterInputBody } from '../codecs/InClusterInputBody';

export const op_connectionInCluster_create = {
  method: 'POST',
  path: '/api/v1/connections/in-cluster',
  entity: 'Connection',
  rootType: 'Connection',
  provides: ['Connection:{id}'],
  invalidates: ['Connection[]'],
  bodyCodec: codec_InClusterInputBody,
  responseCodec: codec_Connection,
} as const satisfies OperationMeta;
