CREATE TABLE public.filtered_addresses (
    address varchar NOT NULL,
    file_version varchar NOT NULL
);
CREATE UNIQUE INDEX filtered_addresses_address_idx ON public.filtered_addresses (address);