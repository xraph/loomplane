import type { OperationMeta } from '../ops';
import { codec_DomainOverview } from '../codecs/DomainOverview';

export const op_domains_overview = {
  method: 'GET',
  path: '/api/v1/domains',
  entity: 'Registration',
  rootType: 'DomainOverview',
  provides: ['Registration:{id}', 'Registration[]'],
  invalidates: [],
  responseCodec: codec_DomainOverview,
} as const satisfies OperationMeta;
