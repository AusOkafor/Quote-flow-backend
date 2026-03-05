-- Restore WiPay as a supported payment processor.
-- WiPay is the only processor that supports JMD, TTD, and BBD.
-- Platform fee does NOT apply to WiPay transactions (0% fee).

-- Restore processor constraint to include wipay
ALTER TABLE payment_accounts
    DROP CONSTRAINT IF EXISTS payment_accounts_processor_check;

ALTER TABLE payment_accounts
    ADD CONSTRAINT payment_accounts_processor_check
        CHECK (processor IN ('stripe', 'paypal', 'wipay'));

-- Reactivate any previously deactivated WiPay accounts
UPDATE payment_accounts
    SET is_active = true
    WHERE processor = 'wipay';
