# go-shorturl

A small URL shortener: one Go binary, standard library only, links kept in
memory. I wrote it as a sample of how I structure an HTTP service in Go, so
it is deliberately small. There is no database and no auth, and links
disappear on restart.

> This is a demo/portfolio project, not client work.

## Running

```sh
go run ./cmd/shorturl
```

Requires Go 1.26 or newer. Configuration comes from the environment:

| Variable           | Default                 | Notes                                      |
|--------------------|-------------------------|--------------------------------------------|
| `ADDR`             | `:8080`                 | listen address                             |
| `BASE_URL`         | `http://localhost:8080` | prefix for `short_url` in responses        |
| `REQUEST_TIMEOUT`  | `5s`                    | per request, answered with 503 when hit    |
| `SHUTDOWN_TIMEOUT` | `15s`                   | how long SIGTERM waits for open requests   |

Logs are JSON lines on stdout.

## API

Create a link. `alias` is optional; without it you get a random 7-character code.

```sh
$ curl -s localhost:8080/api/links -H 'Content-Type: application/json' \
    -d '{"url":"https://go.dev/doc/","alias":"godoc"}'
{"code":"godoc","short_url":"http://localhost:8080/godoc","url":"https://go.dev/doc/","created_at":"2026-10-22T18:54:14.065815511Z","visits":0}
```

`GET /{code}` answers 302 to the target and counts a visit. `GET /api/links/{code}`
returns the same JSON as above with the current visit count. `GET /healthz`
returns 204.

Errors are JSON, `{"error": "..."}`:

| Status | When |
|--------|------|
| 400 | bad JSON, unknown fields, URL not absolute http(s), URL pointing back at `BASE_URL`, invalid alias |
| 404 | unknown code |
| 409 | alias already taken |
| 413 | body over 4 KiB |
| 415 | `Content-Type` is not `application/json` |
| 500 | storage failure; details go to the log, not the client |

## Example session

A real run from October 22, 2026:

```text
$ curl -s localhost:18080/api/links -H 'Content-Type: application/json' -d '{"url":"https://pkg.go.dev/net/http"}'
{"code":"wnwj24t","short_url":"http://localhost:8080/wnwj24t","url":"https://pkg.go.dev/net/http","created_at":"2026-10-22T18:54:14.071015463Z","visits":0}

$ curl -s -o /dev/null -w '%{http_code} %{redirect_url}\n' localhost:18080/godoc
302 https://go.dev/doc/

$ curl -s localhost:18080/api/links/godoc
{"code":"godoc","short_url":"http://localhost:8080/godoc","url":"https://go.dev/doc/","created_at":"2026-10-22T18:54:14.065815511Z","visits":1}

$ curl -s -w ' %{http_code}\n' localhost:18080/api/links -H 'Content-Type: application/json' -d '{"url":"https://example.com/","alias":"godoc"}'
{"error":"alias is already taken"} 409

$ curl -s -w ' %{http_code}\n' localhost:18080/api/links -H 'Content-Type: application/json' -d '{"url":"ftp://example.com/"}'
{"error":"url must use http or https"} 400
```

Server log for the same run (excerpt, JSON lines):

```text
{"time":"2026-10-22T20:54:13.060895991+02:00","level":"INFO","msg":"listening","addr":"127.0.0.1:18080","base_url":"http://localhost:8080"}
{"time":"2026-10-22T20:54:14.065858247+02:00","level":"INFO","msg":"request","method":"POST","path":"/api/links","status":201,"duration_ms":0.198}
{"time":"2026-10-22T20:54:14.075845445+02:00","level":"INFO","msg":"request","method":"GET","path":"/godoc","status":302,"duration_ms":0.037}
{"time":"2026-10-22T20:54:14.09053951+02:00","level":"INFO","msg":"shutting down","cause":"terminated signal received"}
```

The server ran with `ADDR=127.0.0.1:18080` and the default `BASE_URL`.

## Layout

`internal/links` has the `Link` type and the in-memory store. `internal/httpapi`
has the handlers and depends on a three-method `Store` interface, so a
database-backed store could replace `links.Memory` without touching the
handlers. `cmd/shorturl` reads config, wires things up and handles shutdown.

## Checks

```sh
make check   # gofmt, go vet, golangci-lint, go test -race, govulncheck
make cover
```

The tests in `cmd/shorturl` start the real server on a random port and stop
it through context cancellation, the same path SIGTERM takes.

## License

MIT, see [LICENSE](LICENSE).
