#!/bin/bash
set -euo pipefail
export PATH="$PATH:/usr/local/go/bin"
cd /opt/traffic-bot

# Archive installs have no .git. Build in isolation; never overwrite DB or secrets.
stage=$(mktemp -d /opt/traffic-bot/.update.XXXXXX)
trap 'rm -rf -- "$stage"' EXIT
ref=${TRAFFIC_BOT_REF:-main}
if [ -d .git ]; then
  test -z "$(git status --porcelain)" || { echo 'Working tree has changes; refusing to overwrite.'; exit 1; }
  git pull --ff-only
  git archive HEAD | tar -x -C "$stage"
else
  curl -fL --connect-timeout 15 --max-time 180 "https://codeload.github.com/deusaw/traffic-bot/tar.gz/$ref" -o "$stage/source.tgz"
  tar -xzf "$stage/source.tgz" --strip-components=1 -C "$stage"
fi
(cd "$stage" && CGO_ENABLED=1 go test ./... && CGO_ENABLED=1 go build -o traffic-bot.new .)

# Includes hidden vnStat 1.x backups; no routes, proxy, or firewall changes.
if id vnstat >/dev/null 2>&1 && [ -d /var/lib/vnstat ]; then
  chown -R vnstat:vnstat /var/lib/vnstat
fi
cp -p traffic-bot "$stage/traffic-bot.previous"
cp "$stage/traffic-bot.new" ./traffic-bot.new
chmod 755 ./traffic-bot.new
mv ./traffic-bot.new ./traffic-bot
if ! systemctl restart traffic-bot || ! sleep 3 || ! systemctl is-active --quiet traffic-bot; then
  cp "$stage/traffic-bot.previous" ./traffic-bot.rollback
  mv ./traffic-bot.rollback ./traffic-bot
  systemctl restart traffic-bot
  echo 'Update failed; previous binary restored.'
  exit 1
fi
# Explicit source allowlist: preserve database, environment and configuration.
for src in "$stage"/*.go "$stage"/go.mod "$stage"/go.sum "$stage"/install.sh "$stage"/update.sh "$stage"/README*.md; do
  [ ! -f "$src" ] || cp "$src" .
done
echo 'Traffic Bot updated. Database and credentials preserved.'
