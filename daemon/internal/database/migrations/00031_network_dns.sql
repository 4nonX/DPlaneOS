-- +goose Up
-- DNS servers and search domains set in the web interface (internal/netconf
-- renders them into a systemd-resolved drop-in).
CREATE TABLE IF NOT EXISTS network_dns (
    kind     TEXT    NOT NULL DEFAULT 'server',  -- 'server' or 'search'
    position INTEGER NOT NULL,
    value    TEXT    NOT NULL,
    PRIMARY KEY (kind, position)
);

-- +goose Down
DROP TABLE IF EXISTS network_dns;
