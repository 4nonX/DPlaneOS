# PostgreSQL Migration Guide - D-PlaneOS v7.0.0

D-PlaneOS has migrated from SQLite to PostgreSQL for enhanced performance, reliability, and enterprise-grade concurrency.

## Prerequisites

- PostgreSQL server (version 15+ recommended)
- A database named `dplaneos`
- A user named `dplaneos` with full permissions to the `dplaneos` database

## Configuration

The `--db` flag is deprecated and replaced by `--db-dsn`.

**Old command (SQLite):**
```bash
dplaned --db /var/lib/dplaneos/dplaneos.db
```

**New command (PostgreSQL):**
```bash
dplaned --db-dsn "postgres://dplaneos@localhost/dplaneos?sslmode=disable"
```

## Manual Migration (Data Transfer)

If you have existing data in SQLite that you wish to migrate, follow these steps:

1. **Dump SQLite data:**
   Use a tool like `sqlite3` to dump the data as SQL inserts.
   ```bash
   sqlite3 dplaneos.db .dump > dump.sql
   ```
2. **Convert syntax:**
   SQLite syntax (especially for timestamps and autoincrement) is different from PostgreSQL. You will need to manually adjust the SQL file.
3. **Import into PostgreSQL:**
   ```bash
   psql -h localhost -U dplaneos -d dplaneos -f dump.sql
   ```

> [!IMPORTANT]
> Always back up your data before performing a migration.

## Architectural Changes

- SQLite `INTEGER PRIMARY KEY AUTOINCREMENT` -> PostgreSQL `BIGSERIAL PRIMARY KEY`
- SQLite `datetime('now')` -> PostgreSQL `NOW()`
- SQLite `?` placeholders -> PostgreSQL `$1, $2, ...`
- Connection management handled by `pgx/v5`
