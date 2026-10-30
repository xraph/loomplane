# Loomplane 1-Click App listing

Copy for the DigitalOcean Vendor Portal. The Packer template next to this file builds the snapshot; submitting it stays a manual step.

## Summary

Loomplane is a self-hosted deployment platform for the monorepos you already have. This 1-Click App gives you a Droplet with Docker, the Loomplane stack and its images already in place, so you go from nothing to a running dashboard in a few minutes. Caddy gets a certificate for your domain on its own.

## Recommended size

Use an 8 GB Droplet (`s-4vcpu-8gb`). It has room for Postgres, the API and the analysis workers at the same time.

4 GB is the minimum. On a 4 GB Droplet, change `LOOMPLANE_ANALYSIS_CONCURRENCY` to `1` in `/srv/loomplane/.env` (the bundle sets 2) so only one analysis runs at once, then run `cd /srv/loomplane && docker compose up -d` to apply it. Anything smaller will run out of memory.

## Getting started

1. Create a Droplet from the Loomplane image.
2. Point a DNS A record for your domain at the Droplet's IP address. Caddy needs it to get a certificate.
3. SSH in as root. The wizard starts on your first login after the Droplet finishes booting. If you log in earlier, the login message says it's still installing, so log in again in a minute or two.
4. Answer the two questions: the domain Loomplane will answer on, and an email address for certificate notices. The wizard starts the stack and waits until it's healthy.
5. Open the setup link it prints and create the first account.
6. Copy `/srv/loomplane/.env` somewhere safe. It holds the secret key, and without it Loomplane can't decrypt the credentials you store.

If you close the session before the wizard finishes, run `loomplane-setup` yourself. Each Droplet gets its own secret key and database password on first boot, so no two installs share one.

## What's in the image

Ubuntu 24.04 LTS, Docker Engine with the Compose plugin, `ufw` allowing ports 22, 80 and 443, and the pinned Loomplane release in `/srv/loomplane` with its images already pulled.

## Support

Report problems and ask questions at https://github.com/xraph/loomplane/issues.
