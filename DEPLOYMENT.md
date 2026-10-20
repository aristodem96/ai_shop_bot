# Deployment

Two ways to run the stack in Docker: on a laptop, and on a server with a domain
so Monobank can deliver payment webhooks.

Both use the same image. The production setup is an overlay
(`docker-compose.prod.yml`) on top of the local one, never a replacement.

---

## Local: everything in Docker, no domain

```bash
cp .env.example .env     # TELEGRAM_BOT_TOKEN, ANTHROPIC_API_KEY, MONOBANK_TOKEN, ADMIN_CHAT_ID
make up                  # Postgres, Redis and the bot
make docker-seed         # load the demo catalogue
make logs
```

Without a public URL there is no webhook, and that is fine: the bot polls the
status of every unpaid invoice every `PAYMENT_RECONCILE_EVERY` (30s), so a
payment is picked up within half a minute. The poll is also what catches a
webhook that was lost in production.

To try the webhook itself on a laptop, expose port 8090 through a tunnel and
put its address in `.env`:

```bash
cloudflared tunnel --url http://localhost:8090      # or: ngrok http 8090
# PUBLIC_URL=https://<the address it prints>
make up        # recreates the bot with the new setting; the database stays
```

(`make down` stops the stack and keeps its data. `make reset` stops it **and
deletes the database and uploaded price lists**: use it only to start from
scratch. It also clears old images and the build cache, as `make prune` does
alone.)

Monobank only delivers to `https://` addresses, which every tunnel provides.

---

## Production: a server with a domain

Everything runs in Docker: the bot, PostgreSQL, Redis, and Caddy as the
TLS-terminating reverse proxy. Caddy obtains and renews the certificate on its
own. The only things installed on the host are Docker and a firewall rule.

**You need:** a server with a public IP (1–2 vCPU, 2 GB RAM), a domain you
control, and SSH access as a non-root user with `sudo`.

**Check the disk before you pay for a month.** The database, a price list
import and a car's catalogue all write a lot, and a cheap VPS whose storage
is throttled makes the bot hang, not just slow down. A plain SSD writes
100 MB/s and more:

```bash
dd if=/dev/zero of=/root/disktest bs=1M count=64 oflag=direct status=progress; rm -f /root/disktest
```

If this takes more than a second or two (a few MB/s), ask the host to move
the server or pick another; `iostat -x 1` showing `w_await` in the tens or
hundreds of milliseconds, or `hung_task` warnings in `dmesg`, say the same.

### 1. Point the domain at the server

```
Type   Name       Value
A      shop-bot   203.0.113.45        your server's public IP
```

Check it resolves before going on; Let's Encrypt will not issue a certificate
otherwise:

```bash
dig +short shop-bot.example.com
```

### 2. Install Docker and open the firewall

```bash
ssh user@203.0.113.45
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker $USER      # log out and back in afterwards

sudo ufw allow OpenSSH
sudo ufw allow 80/tcp              # Let's Encrypt's HTTP challenge
sudo ufw allow 443/tcp             # the webhook
sudo ufw enable
```

Nothing else needs to be open. The bot reaches Telegram by long polling, an
outgoing connection, so no port is needed for the chat itself.

### 3. Get the code and configure it

```bash
sudo mkdir -p /opt/ai_shop_bot && sudo chown $USER /opt/ai_shop_bot
git clone https://github.com/<you>/ai_shop_bot.git /opt/ai_shop_bot
cd /opt/ai_shop_bot
cp .env.prod.example .env
nano .env
```

Fill in every empty value. Generate the passwords with `openssl rand -hex 32`:
the Postgres password goes inside a URL, where the `/` and `+` that base64
produces would break it.
`COMPOSE_FILE` in that file makes every `docker compose` command use both
compose files, so the commands below need no `-f` flags.

`ADMIN_CHAT_ID` is the chat where managers get paid orders. You learn it from
the bot, but compose will not start without it: put `0` there first (no
managers' chat), start the stack, send `/id` to the bot from that chat (a
private chat or a managers' group; in a group, add the bot first), put the
number in `.env`, and run `docker compose up -d` again.

**A managers' group needs the bot as an administrator.** In a group a bot
sees only commands and replies to its own messages unless it is an admin,
so price lists sent there and a price typed without "Reply" would never
reach it. Make it an admin of the group (no rights beyond the default are
needed), or turn its privacy mode off in @BotFather (`/setprivacy` →
Disable). Everyone in that group can use the managers' commands, so add
managers only.

`AUTODB_USER` and `AUTODB_PASSWORD` turn on the Auto-DB parts catalogue. The
account lets requests in from one IP address only: put the server's address
in the Auto-DB dashboard first, or every call is refused (the log says
`this IP address is not allowed`). Find the address with
`curl -4 ifconfig.me` on the server. `SHOP_PICKUP_ADDRESS` — the shop's
address and hours — offers pickup at checkout; empty, it is Nova Poshta only.

### 4. Start it

```bash
docker compose up -d --build
docker compose run --rm --entrypoint /seed bot     # demo catalogue, once
docker compose ps
curl https://shop-bot.example.com/healthz          # {"status":"ok"}
```

For a real shop, skip the demo catalogue and load the stock instead: send
the export from 1C, or a supplier's price list, to the managers' chat — the
bot reads its columns, asks to confirm them and to name the supplier, and
shows a report before anything changes.
[docs/import/README.md](docs/import/README.md) walks through it. Then the
vehicle directory for the makes Auto-DB lacks, once:
`docker compose run --rm --entrypoint /import bot -vehicles /data/vehicles.csv`.

There is nothing to configure in the Monobank cabinet: the webhook address is
sent with every invoice, built from `BOT_DOMAIN`.

Only `/webhooks/monobank` and `/healthz` are reachable from outside; Caddy
answers 404 to everything else. Postgres and Redis publish no ports at all.

### Updating

```bash
cd /opt/ai_shop_bot
git pull
docker compose up -d --build
```

Migrations run on start. Uploaded price lists waiting for *Apply* live in
the `imports` volume and the managers' dialogs in Redis, so a deploy midway
through an import or a vehicle matching session loses neither. The bot
finishes in-flight updates on `SIGTERM` (`stop_grace_period: 70s`), so a
deploy does not cut a buyer off mid-checkout.

### Disk

Every `docker compose up --build` leaves the previous image and more build
cache behind: gigabytes after a few weeks of updates. `docker system df`
shows it, and `make prune` frees it without touching the database or the
certificates. `make reset` frees it too, but also deletes the database:
use it only to start the shop from scratch, never to fix a build, which it
only makes slower by clearing the cache.

Never run `docker compose down -v` or `docker volume prune` here: besides
the database they delete Caddy's certificate, and Let's Encrypt issues one
for a domain only five times a week. `make reset` keeps it.

### Logs

```bash
docker compose logs -f bot          # JSON, one line per event
docker compose logs -f caddy        # certificate issuance, webhook requests
```

Every consultant turn logs its token counts (`input_tokens`,
`cache_read_tokens`, `output_tokens`), which is where the model bill comes from.

### Backups

The data lives in the `ai_shop_bot_pgdata` Docker volume, not in the project
directory, so backing up `/opt/ai_shop_bot` backs up nothing. Dump the database
instead (`shop shop` is `POSTGRES_USER` and `POSTGRES_DB` from `.env`):

```bash
docker compose exec -T postgres pg_dump -U shop shop | gzip > backup-$(date +%F).sql.gz
```

Restore into a fresh stack before the bot first starts, since the bot
creates the tables on start and the dump would collide with them:

```bash
docker compose up -d postgres
gunzip -c backup-2026-10-16.sql.gz | docker compose exec -T postgres psql -U shop shop
docker compose up -d
```

Redis needs no backup: it holds conversations, checkout forms and rate limits,
all of which are fine to lose.
