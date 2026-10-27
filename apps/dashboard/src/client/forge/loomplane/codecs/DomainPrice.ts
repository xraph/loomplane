import type { Codec } from '../codec-runtime';

export const codec_DomainPrice: Codec = {"kind": "object", "fields": {"currency": {"ts": "currency"}, "registration_cost": {"ts": "registrationCost"}, "renewal_cost": {"ts": "renewalCost"}}, "required": ["currency", "registration_cost", "renewal_cost"]};
