import type { Codec } from '../codec-runtime';
import { codec_RegistrationInputBody_project_ids } from './RegistrationInputBody.project_ids';
import { codec_RegistrationInputBody_registrant } from './RegistrationInputBody.registrant';

export const codec_RegistrationInputBody: Codec = {"kind": "object", "fields": {"auto_renew": {"ts": "autoRenew"}, "connection_id": {"ts": "connectionId"}, "name": {"ts": "name"}, "privacy_mode": {"ts": "privacyMode"}, "project_ids": {"ts": "projectIds", "codec": () => codec_RegistrationInputBody_project_ids}, "registrant": {"ts": "registrant", "codec": () => codec_RegistrationInputBody_registrant}}, "required": ["connection_id", "name"]};
