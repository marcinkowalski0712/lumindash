# lumindash

A fast, single-binary replacement for the Zabbix PHP web frontend, written in Go.

> **Not affiliated with Zabbix LLC or the Zabbix Group.**

## Requirements

- Go 1.22+
- PostgreSQL with an existing Zabbix database (6.0 – 7.2; 8.0 alpha experimental)
- Access to the Zabbix JSON-RPC API (for write operations only)

## Build

```sh
git clone git@github.com:marcinkowalski0712/lumindash.git
cd lumindash
go mod download
go build -o lumindash ./cmd/lumindash
```

The result is a single self-contained binary with templates and static assets embedded.

### Cross-compile

```sh
GOOS=linux GOARCH=amd64 go build -o lumindash-linux-amd64 ./cmd/lumindash
```

## Run

```sh
ZBX_DB_HOST=localhost \
ZBX_DB_USER=zabbix \
ZBX_DB_PASS=zabbix \
ZBX_DB_NAME=zabbix \
ZBX_API_URL=http://your-zabbix/api_jsonrpc.php \
./lumindash
```

Then open [http://localhost:8090](http://localhost:8090).

## Docker

```sh
docker build -t lumindash .

docker run -p 8090:8090 \
  -e ZBX_DB_HOST=your-db-host \
  -e ZBX_DB_USER=zabbix \
  -e ZBX_DB_PASS=zabbix \
  -e ZBX_DB_NAME=zabbix \
  -e ZBX_API_URL=http://your-zabbix/api_jsonrpc.php \
  lumindash
```

### docker-compose (dev)

Starts lumindash alongside a fresh PostgreSQL instance:

```sh
docker compose up
```

> The bundled Postgres is empty — it is only useful for development.  
> For production, point `ZBX_DB_*` at your existing Zabbix database.

## Configuration

All configuration is via environment variables.

| Variable | Default | Description |
|---|---|---|
| `ZBX_DB_HOST` | `localhost` | PostgreSQL host |
| `ZBX_DB_PORT` | `5432` | PostgreSQL port |
| `ZBX_DB_USER` | `zabbix` | PostgreSQL user |
| `ZBX_DB_PASS` | `zabbix` | PostgreSQL password |
| `ZBX_DB_NAME` | `zabbix` | PostgreSQL database name |
| `ZBX_API_URL` | `http://zabbix-server/api_jsonrpc.php` | Zabbix JSON-RPC endpoint |
| `ZBX_API_USER` | `Admin` | Zabbix API username |
| `ZBX_API_PASS` | `zabbix` | Zabbix API password |
| `LISTEN_ADDR` | `:8090` | Address and port to listen on |
| `READ_TIMEOUT` | `30s` | HTTP read timeout |
| `WRITE_TIMEOUT` | `30s` | HTTP write timeout |

## Health check

```sh
curl http://localhost:8090/healthz
```

```json
{
  "status": "ok",
  "db": "ok",
  "zabbix_version": "7.2.1",
  "zabbix_version_raw": 7002001,
  "zabbix_stability": "stable",
  "adapter": "Adapter72",
  "timescaledb": false,
  "partitioned_history": false,
  "schema_manifest_cached": false,
  "lumindash_version": "0.1.0",
  "supported": true
}
```

## Supported Zabbix versions

| Version | Support |
|---|---|
| 6.0 LTS, 6.2 | Stable |
| 6.4 LTS | Stable |
| 7.0 LTS | Stable |
| 7.2 | Stable |
| 8.0 alpha | Experimental |
| < 6.0 | Not supported |

lumindash detects the Zabbix version on startup and selects the appropriate query adapter automatically.

## License

MIT — see [LICENSE](LICENSE).
