CREATE TABLE public.api_keys (
	api_key varchar NOT NULL,
	lsp_ids jsonb NOT NULL,
	api_user varchar NOT NULL
);
CREATE UNIQUE INDEX api_keys_api_key_idx ON public.api_keys (api_key);