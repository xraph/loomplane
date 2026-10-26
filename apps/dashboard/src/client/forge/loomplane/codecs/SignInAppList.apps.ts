import type { Codec } from '../codec-runtime';
import { codec_SignInApp } from './SignInApp';

export const codec_SignInAppList_apps: Codec = {"kind": "array", "items": () => codec_SignInApp};
