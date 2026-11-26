Quarry turns the data you already have into a queryable project. This page takes you from nothing to your first query in about five minutes. If you are upgrading from 1.x, read [Upgrading](https://example.test/docs/getting-started/upgrading) first.

**Prerequisites.** A 64-bit Linux, macOS or Windows machine, and about 200 MB of free disk. No database server is needed; Quarry embeds its own. See [system requirements](https://example.test/docs/ops/requirements "System requirements") for the full list.

## 1. Install the CLI

The quickest path is the install script, which needs neither `sudo` nor a package manager. It verifies the download against the published checksums before it installs anything.

```
curl -fsSL https://get.quarry.dev/install.sh | sh
quarry --version
```

Homebrew, Scoop and container images are covered in [**the installation guide**](https://example.test/docs/getting-started/install), along with how to pin a version and how to verify a release signature by hand.

### Shell completion

Completion scripts for bash, zsh and fish ship with the binary. Run `quarry completion --help` or read [shell completion](https://example.test/docs/cli/completion).

## 2. Create a project

A project is a directory holding a `quarry.toml` and the sources it reads. Create one with:

```
mkdir orders && cd orders
quarry init --name orders
```

The generated file is commented line by line; every key is described in the [quarry.toml reference](https://example.test/docs/config/file). Projects can also inherit settings from a [*profile*](https://example.test/docs/config/profiles), which is how most teams share connection settings.

## 3. Add a source

Sources are where your data lives: CSV and Parquet files, a Postgres or MySQL database, or an HTTP API. Point Quarry at a sample file to start:

```
curl -fsSLO https://get.quarry.dev/samples/orders.csv
quarry source add orders.csv --name orders
```

Quarry infers a schema from the first 10,000 rows. You can review and override it; see [schemas](https://example.test/docs/concepts/schemas) and [overriding an inferred schema](https://example.test/guides/schema-overrides).

### Databases

For a database source, store the connection string as a secret and reference it by name. Supported engines and their options are listed under [Postgres](https://example.test/docs/sources/postgres), [MySQL](https://example.test/docs/sources/mysql), [SQLite](https://example.test/docs/sources/sqlite) and [HTTP APIs](https://example.test/docs/sources/http).

## 4. Run your first query

Queries are plain SQL with a few extensions for time series and nested data:

```
SELECT customer_id, count(*) AS orders, sum(total) AS revenue
FROM orders
WHERE placed_at >= now() - interval '30 days'
GROUP BY customer_id
ORDER BY revenue DESC
LIMIT 10;
```

Save it as `top-customers.sql` and run `quarry query top-customers.sql`. Results print as a table by default; add `--format json` or `--format csv` for machine output. The [query API](https://example.test/docs/api/query) runs the same file over HTTP.

### How results are cached

Results are cached per source version, so a repeated query over unchanged data returns instantly. Read [caching](https://example.test/docs/concepts/caching) to tune or disable it.

## Next steps

- [Build a dashboard](https://example.test/guides/dashboards) from saved queries.
- [Schedule a query](https://example.test/guides/scheduling) and send the results to Slack or email.
- [Share a project](https://example.test/docs/concepts/permissions) with read-only permissions.
- [Deploy Quarry](https://example.test/docs/ops/deploy) as a service for your team.

[Edit this page](https://github.com/quarry-dev/docs/edit/main/getting-started.md) [Next: Installation →](https://example.test/docs/getting-started/install)

Was this page helpful?