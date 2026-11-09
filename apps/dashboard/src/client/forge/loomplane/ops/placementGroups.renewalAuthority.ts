import type { OperationMeta } from '../ops';
import { codec_TLSRenewalAuthority } from '../codecs/TLSRenewalAuthority';
import { codec_TLSRenewalAuthorityInputBody } from '../codecs/TLSRenewalAuthorityInputBody';

export const op_placementGroups_renewalAuthority = {
  method: 'POST',
  path: '/api/v1/placement-groups/renewal-authorities',
  entity: 'TLSRenewalAuthority',
  rootType: 'TLSRenewalAuthority',
  provides: ['TLSRenewalAuthority:{id}'],
  invalidates: ['TLSRenewalAuthority[]'],
  bodyCodec: codec_TLSRenewalAuthorityInputBody,
  responseCodec: codec_TLSRenewalAuthority,
} as const satisfies OperationMeta;
