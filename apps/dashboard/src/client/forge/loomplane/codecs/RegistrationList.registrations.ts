import type { Codec } from '../codec-runtime';
import { codec_Registration } from './Registration';

export const codec_RegistrationList_registrations: Codec = {"kind": "array", "items": () => codec_Registration};
