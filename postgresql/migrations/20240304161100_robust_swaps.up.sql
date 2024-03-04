CREATE TABLE public.redeem_sync (
    block_height bigint NOT NULL
);
INSERT INTO public.redeem_sync (block_height) VALUES (833123);

ALTER TABLE public.swap_payments
ADD COLUMN lock_height bigint NULL,
ADD COLUMN confirmation_height bigint NULL,
ADD COLUMN utxos jsonb NOT NULL DEFAULT '[]',
ADD COLUMN redeem_confirmed boolean NULL;

CREATE INDEX swap_payments_in_progress ON public.swap_payments (redeem_confirmed, confirmation_height);