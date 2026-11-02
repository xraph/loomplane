import type { Codec } from '../codec-runtime';

export const codec_MoneyComponent: Codec = {"kind": "object", "fields": {"category": {"ts": "category"}, "hourly_micro_usd": {"ts": "hourlyMicroUsd"}, "name": {"ts": "name"}, "price_checked_at": {"ts": "priceCheckedAt"}, "quantity": {"ts": "quantity"}, "source": {"ts": "source"}}, "required": ["category", "name", "quantity", "source"]};
