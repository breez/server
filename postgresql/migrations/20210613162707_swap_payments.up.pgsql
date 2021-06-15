
CREATE TABLE public.swap_payments (
    payment_hash varchar NOT NULL,
    payment_request varchar NOT NULL,
    payment_preimage varchar NULL,
    txid jsonb NOT NULL default '[]',
    CONSTRAINT payment_hash_pkey PRIMARY KEY (payment_hash)
);
CREATE INDEX swap_payments_status ON public.swap_payments (txid);