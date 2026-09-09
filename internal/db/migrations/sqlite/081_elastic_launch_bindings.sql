CREATE TABLE IF NOT EXISTS elastic_launch_bindings (
    reservation_id TEXT PRIMARY KEY REFERENCES elastic_reservations(id),
    node_id TEXT NOT NULL CHECK (length(node_id) > 0),
    request_digest TEXT NOT NULL CHECK (length(request_digest) = 64)
);
