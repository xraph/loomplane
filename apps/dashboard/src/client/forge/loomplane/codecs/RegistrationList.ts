import type { Codec } from '../codec-runtime';
import { codec_RegistrationList_registrations } from './RegistrationList.registrations';

export const codec_RegistrationList: Codec = {"kind": "object", "fields": {"registrations": {"ts": "registrations", "codec": () => codec_RegistrationList_registrations}}, "required": ["registrations"]};
