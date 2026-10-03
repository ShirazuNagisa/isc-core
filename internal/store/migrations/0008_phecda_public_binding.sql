-- Associate a Phecda deployment with the GUI/public-service binding.
ALTER TABLE phecda_deployments ADD COLUMN public_service_id TEXT;
