#!/bin/bash
set -euo pipefail
export PATH="$PATH:/usr/local/go/bin"
cd /opt/traffic-bot

# Serialize helper/source replacement with reconciliation. The Bot stays online
# and reports a short monitoring sync while kernel counters keep accumulating.
exec 9>/run/traffic-bot-monitor.lock
flock -w 60 9 || { echo 'Another monitoring operation is still running.'; exit 1; }

# Archive installs have no .git. Build in isolation; never overwrite DB or secrets.
stage=$(mktemp -d /opt/traffic-bot/.update.XXXXXX)
trap 'rm -rf -- "$stage"' EXIT
if [ -f monitor_setup.py ]; then cp -p monitor_setup.py "$stage/monitor_setup.previous"; fi
ref=${TRAFFIC_BOT_REF:-main}
migrate_sum=0
if [ -d .git ]; then
  git diff --quiet && git diff --cached --quiet || { echo 'Tracked files have changes; refusing to overwrite.'; exit 1; }
  git fetch origin "$ref"
  # Early installers generated go.sum before the repository tracked it.
  # Only migrate an identical generated file; never overwrite different content.
  if [ -f go.sum ] && ! git ls-files --error-unmatch go.sum >/dev/null 2>&1; then
    git show FETCH_HEAD:go.sum > "$stage/expected-go.sum"
    cmp -s go.sum "$stage/expected-go.sum" || { echo 'Untracked go.sum differs; refusing to overwrite.'; exit 1; }
    migrate_sum=1
  fi
  git archive FETCH_HEAD | tar -x -C "$stage"
else
  curl -fL --connect-timeout 15 --max-time 180 "https://codeload.github.com/deusaw/traffic-bot/tar.gz/$ref" -o "$stage/source.tgz"
  tar -xzf "$stage/source.tgz" --strip-components=1 -C "$stage"
fi
(cd "$stage" && CGO_ENABLED=1 go test ./... && CGO_ENABLED=1 go build -o traffic-bot.new .)

# Do not change live helper sources or move generated files until tests/build
# succeed. Keep the helper compatible if the binary needs to roll back.
if [ -d .git ]; then
  git diff --quiet && git diff --cached --quiet || { echo 'Tracked files changed during the build; refusing to overwrite.'; exit 1; }
  if [ "$migrate_sum" = 1 ]; then mv go.sum "$stage/original-go.sum"; fi
  if ! git merge --ff-only FETCH_HEAD; then
    if [ "$migrate_sum" = 1 ]; then mv "$stage/original-go.sum" go.sum; fi
    exit 1
  fi
fi

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
  if [ -f "$stage/monitor_setup.previous" ]; then cp -p "$stage/monitor_setup.previous" ./monitor_setup.py; fi
  systemctl restart traffic-bot
  echo 'Update failed; previous binary restored.'
  exit 1
fi
# Explicit source allowlist: preserve database, environment and configuration.
for src in "$stage"/*.go "$stage"/go.mod "$stage"/go.sum "$stage"/install.sh "$stage"/update.sh "$stage"/monitor_setup.py "$stage"/README*.md; do
  [ ! -f "$src" ] || cp "$src" .
done
if [ -f /etc/traffic-bot-monitor.json ]; then
  python /opt/traffic-bot/monitor_setup.py --install-services
fi
echo 'Traffic Bot updated. Database and credentials preserved.'
