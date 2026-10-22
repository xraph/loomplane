import type { Codec } from '../codec-runtime';

export const codec_RegistrationApprovalBody: Codec = {"kind": "object", "fields": {"currency": {"ts": "currency"}, "registration_cost": {"ts": "registrationCost"}, "review": {"ts": "review"}}, "required": ["currency", "registration_cost", "review"]};
