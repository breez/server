CREATE INDEX tx_notifications_boltz_reverse_swap_timeout
ON public.tx_notifications
USING btree ((((additional_info ->> 'timeout_block_height'::text))::integer)) WHERE (tx_type=1 AND status=1);
