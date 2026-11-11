<p align="center">
  <img src="docs/logo.svg" alt="Catalyst" width="320">
</p>

<h1 align="center">ledger-backend</h1>

<p align="center">
  The Go API behind Catalyst, a personal finance app: accounts, cards, transactions, EMIs, investments, budgets, a vehicle garage and an AI copilot.
</p>

---

## What it does

- **Parses bank SMS into transactions.** [ledger-api](../ledger-api) stores raw messages in MongoDB. This service watches for new ones (a change stream), asks an LLM to extract the amount, vendor and category, and files them as transactions.
- **Tracks the whole picture.** Accounts, credit cards, EMIs and loans, mutual funds, stocks, mandates, debts, budgets and net-worth snapshots.
- **Looks after you.** A copilot runs rules every 15 minutes and raises nudges. It also does price alerts, vehicle paper reminders and push notifications (Web Push).
- **Answers questions.** The dashboard chat uses an LLM over your own data.
- **One backend, one database per person.** The `X-API-Key` header picks whose cluster answers. Nobody can reach anyone else's data.

## Stack

| | |
|---|---|
| Language | Go 1.26, standard library `net/http` |
| Database | MongoDB (Atlas), driver v2, one cluster per person |
| LLM | Gemini (parsing and chat), via an OpenAI-style client in `internal/nim` |
| Market data | Yahoo chart API (stocks), mutual fund NAVs |
| Files | S3 (vehicle papers and photos), optional |
| Push | Web Push with VAPID keys |

## Layout

```
cmd/
  backend/      the server (this is what runs in production)
  importcard/   one-off: import card data from an old database
  importstmt/   one-off: import bank statements
  vapidgen/     prints a fresh VAPID key pair
internal/
  middleware/   API key check and the per-person router
  module/<x>/   one folder per feature: model, service, controller, routes
  nim/          LLM client
  httpx/        JSON helpers
docs/           README assets
```

Every module follows the same shape: `Service` (logic and Mongo), `Controller` (HTTP), `RegisterRoutes`. They are wired in `build()` in [cmd/backend/main.go](cmd/backend/main.go), once per person.

## Quick start

```bash
cp .env.example .env     # or create .env by hand, see below
go run ./cmd/backend
```

The server listens on `:$PORT`, and every route lives under `/finance/api`, for example `http://localhost:8081/finance/api/dashboard`.

Tests:

```bash
go test ./...
```

## Configuration

Settings come from the environment, and a `.env` file in the working directory is loaded if present (real environment variables win).

### Per person

A person is switched on when `<NAME>_SECRET` is set. Today the names are `SANGEETH` and `SILPA`. To add someone, add their name to the list in `main.go` and set the variables below.

| Variable | Required | Meaning |
|---|---|---|
| `<NAME>_SECRET` | yes | Their API key. Sent as `X-API-Key`. Must be unique per person. |
| `<NAME>_MONGODB_URI` | yes | Their Atlas connection string |
| `<NAME>_MONGODB_DB` | yes | Database name, e.g. `ledger` |
| `<NAME>_HOLDER` | yes | Name on their bank accounts, so moves between own accounts read as transfers |
| `<NAME>_PARSE_HINT` | no | Extra SMS-parsing rules for the LLM |

### Shared

| Variable | Required | Meaning |
|---|---|---|
| `PORT` | yes | HTTP port (the Dockerfile exposes 8081) |
| `GEMINI_API_KEY`, `GEMINI_MODEL` | yes | LLM used to parse messages into transactions and for dashboard chat |
| `VAPID_PUBLIC_KEY`, `VAPID_PRIVATE_KEY`, `VAPID_SUBJECT` | for push | Generate with `go run ./cmd/vapidgen` |
| `S3_BUCKET`, `AWS_REGION` | for uploads | Without a bucket, file uploads answer 503 |
| `YAHOO_CHART_URL` | no | Override the stock price source |
| `INFLATION_PCT` | no | Inflation used in projections |

Example `.env` (values are placeholders):

```env
PORT=8081
GEMINI_API_KEY=...
GEMINI_MODEL=gemini-3.1-flash-lite

SANGEETH_SECRET=<long random string>
SANGEETH_MONGODB_URI=mongodb+srv://<user>:<password>@<cluster>/?retryWrites=true&w=majority
SANGEETH_MONGODB_DB=ledger
SANGEETH_HOLDER=<NAME ON BANK ACCOUNT>

SILPA_SECRET=<a different long random string>
SILPA_MONGODB_URI=...
SILPA_MONGODB_DB=ledger
SILPA_HOLDER=...
```

> Never commit real keys or passwords. If a secret has been shared or committed, rotate it.

## API

All routes need the `X-API-Key` header, or the server answers `401`. The paths below are relative to `/finance/api`.

| Area | Routes |
|---|---|
| Dashboard | `GET /dashboard`, `/dashboard/networth`, `/dashboard/daily`, `/dashboard/advice`, `/dashboard/chats`; `POST /dashboard/chat` |
| Transactions | `GET/POST /transactions`, `PATCH/DELETE /transactions/{id}`, `POST /transactions/search`, `GET /summary` |
| Raw messages | `GET /raw-transactions/unparsed`, `POST /raw-transactions/{id}/parse` |
| Accounts | `GET/POST /accounts`, `GET/PATCH /accounts/{id}` |
| Cards | `GET/POST /cards`, `PATCH /cards/{id}`, `/cards/{id}/activity`, `/cards/{id}/payments` |
| Categories, vendors | `/categories`, `/vendors` (list, create, patch, search, aliases, merge) |
| EMIs and debts | `/emis` (schedule, impact, payoff, payments, rates, balance), `/debts` |
| Investments | `/funds` (SIPs, exit, re-enter, refresh), `/stocks`, `/portfolio` (income, target), `/alerts` |
| Planning | `/budgets`, `/mandates`, `/profile`, `/views`, `/snapshots` |
| Copilot | `/copilot` (nudges, wants, mode, mutes, check), `/insights` |
| Reminders | `/jogger` (lists, push subscribe, test, unsubscribe) |
| Garage | `/vehicles` (logs, files) |
| Maintenance | `POST /maintenance/purge` |

Routes are registered in each module's `RegisterRoutes`, so that is the source of truth.

## Background jobs (per person)

| Job | How often |
|---|---|
| Parse new raw messages | on insert (change stream), plus a catch-up on start |
| Copilot rules | every 15 min and after each parsed message |
| Net-worth snapshot | every 15 min |
| Price alerts | every 5 min |
| Reminder / push check | every 30 s |
| Vehicle paper reminders | hourly |
| Delete raw messages older than 5 days | hourly (transactions stay) |

## Docker

```bash
docker build -t ledger-backend .
docker run --env-file .env -p 8081:8081 ledger-backend
```

The image is a small static binary on Alpine and runs as a non-root user. Settings come from the environment at run time and are never baked in.

## Deploying

- Allow the server's IP in **Atlas, Network Access**, for every person's cluster. The service pings each cluster at startup and exits if one is unreachable.
- Put a reverse proxy (nginx) in front and forward to `127.0.0.1:$PORT`.
- A **502 from nginx** means the process is not running or the port does not match. Check `ss -ltnp | grep <PORT>` and the process logs. A missing variable or an unreachable cluster stops the server at startup with a clear message.

## Related

- `finance-frontend`: the Next.js app. It signs people in with a username and PIN, then calls this API with their key.
- `ledger-api`: receives the SMS and writes raw messages for this service to parse.
