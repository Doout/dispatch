-- Fetch recent history without sorting the saved deployment snapshots.
CREATE INDEX deployments_created_at ON deployments(created_at DESC);
