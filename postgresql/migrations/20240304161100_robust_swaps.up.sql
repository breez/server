ALTER TABLE public.swap_payments
ADD COLUMN lock_height bigint NULL,
ADD COLUMN confirmation_height bigint NULL,
ADD COLUMN utxos jsonb NOT NULL DEFAULT '[]',
ADD COLUMN redeem_confirmed boolean NULL;

CREATE INDEX swap_payments_in_progress ON public.swap_payments (redeem_confirmed, confirmation_height);