CREATE TABLE public.breez_info (
	"key" text NOT NULL,
	"timestamp" timestamp with time zone NOT NULL,
	value jsonb NOT NULL
);
CREATE UNIQUE INDEX breez_info_key_idx ON public.breez_info ("key","timestamp");