CREATE TABLE public.deviceid_nodeid (
    nodeid bytea NOT NULL,
    deviceid varchar NOT NULL,
    first_registration timestamp NOT NULL,
    CONSTRAINT deviceid_nodeid_pkey PRIMARY KEY (nodeid)
);
CREATE INDEX deviceid_nodeid_deviceid_idx ON public.deviceid_nodeid (deviceid);
