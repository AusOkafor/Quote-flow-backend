-- Remove WiPay from payment_accounts processor constraint
ALTER TABLE payment_accounts
    DROP CONSTRAINT IF EXISTS payment_accounts_processor_check;

ALTER TABLE payment_accounts
    ADD CONSTRAINT payment_accounts_processor_check
        CHECK (processor IN ('stripe', 'paypal'));

-- Soft-delete any existing WiPay accounts (don't hard delete — preserve history)
UPDATE payment_accounts SET is_active = false WHERE processor = 'wipay';
