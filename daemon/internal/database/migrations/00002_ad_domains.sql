-- +goose Up

-- ── Active Directory multi-forest domain registry ──────────────────────────
-- Each row represents one AD forest/domain. The primary domain may reference
-- ldap_config (id=1) or exist only here. Multiple rows enable multi-forest
-- IDMAP ranges (e.g. CORP mapped 10000-999999, PARTNER mapped 2000000-2999999).
CREATE TABLE IF NOT EXISTS ad_domains (
    id               BIGSERIAL PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE,      -- short name, e.g. "CORP" or "PARTNER"
    realm            TEXT NOT NULL,             -- Kerberos realm (FQDN, uppercase)
    server           TEXT NOT NULL DEFAULT '',  -- DC FQDN or IP
    bind_dn          TEXT NOT NULL DEFAULT '',
    bind_password    TEXT NOT NULL DEFAULT '',
    idmap_backend    TEXT NOT NULL DEFAULT 'rid', -- rid, ad, autorid, tdb
    idmap_low        BIGINT NOT NULL DEFAULT 10000,
    idmap_high       BIGINT NOT NULL DEFAULT 999999,
    domain_joined    BOOLEAN NOT NULL DEFAULT false,
    domain_joined_at TIMESTAMPTZ,
    kinit_principal  TEXT NOT NULL DEFAULT '',  -- machine account, e.g. "nas$@CORP.EXAMPLE.COM"
    last_kinit_at    TIMESTAMPTZ,
    kinit_ok         BOOLEAN NOT NULL DEFAULT false,
    enabled          BOOLEAN NOT NULL DEFAULT true,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ad_domains_name  ON ad_domains(name);
CREATE INDEX IF NOT EXISTS idx_ad_domains_realm ON ad_domains(realm);

-- +goose Down

DROP TABLE IF EXISTS ad_domains;
