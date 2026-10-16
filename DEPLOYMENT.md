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
deletes its volumes**, database included: use it only to start from scratch.)

Monobank only delivers to `https://` addresses, which every tunnel provides.

---

## Production: a server with a domain

Everything runs in Docker: the bot, PostgreSQL, Redis, and Caddy as the
TLS-terminating reverse proxy. Caddy obtains and renews the certificate on its
own. The only things installed on the host are Docker and a firewall rule.

**You need:** a server with a public IP (1 vCPU / 1 GB RAM is plenty), a domain
you control, and SSH access as a non-root user with `sudo`.

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

Fill in every empty value. Generate the passwords with `openssl rand -base64 32`.
`COMPOSE_FILE` in that file makes every `docker compose` command use both
compose files, so the commands below need no `-f` flags.

`ADMIN_CHAT_ID` is the chat where managers get paid orders. Start the stack
once, send `/id` to the bot from that chat (a private chat or a managers'
group), put the number in `.env`, and run `docker compose up -d` again.

### 4. Start it

```bash
docker compose up -d --build
docker compose run --rm --entrypoint /seed bot     # demo catalogue, once
docker compose ps
curl https://shop-bot.example.com/healthz          # {"status":"ok"}
```

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

Migrations run on start. The bot finishes in-flight updates on `SIGTERM`
(`stop_grace_period: 70s`), so a deploy does not cut a buyer off mid-checkout.

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
instead:

```bash
docker compose exec -T postgres pg_dump -U shop shop | gzip > backup-$(date +%F).sql.gz
```

Restore into a fresh stack:

```bash
gunzip -c backup-2026-10-16.sql.gz | docker compose exec -T postgres psql -U shop shop
```

Redis needs no backup: it holds conversations, checkout forms and rate limits,
all of which are fine to lose.
