CREATE TABLE public.tx_notifications (
	id uuid NOT NULL,
	tx_type int2 NOT NULL,
	status int2 NOT NULL,
	additional_info jsonb NULL,
	title varchar NOT NULL,
	body varchar NOT NULL,
	device_id varchar NOT NULL,
	tx_hash bytea NOT NULL,
	script bytea NOT NULL,
	block_height_hint int4 NOT NULL,
	tx bytea NULL,
	block_height int4 NULL,
	block_hash bytea NULL,
	tx_index int2 NULL,
	CONSTRAINT tx_notifications_pkey PRIMARY KEY (id)
);