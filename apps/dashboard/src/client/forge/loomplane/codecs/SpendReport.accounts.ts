import type { Codec } from '../codec-runtime';
import { codec_AccountSpend } from './AccountSpend';

export const codec_SpendReport_accounts: Codec = {"kind": "array", "items": () => codec_AccountSpend};
