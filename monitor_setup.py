#!/usr/bin/env python
"""Optional X-UI accounting integration. Python 2.7/3, standard library only.

--plan: read-only validation; --apply: preserve identity, add socket marks and
counter-only rules; --rollback: restore only our template change and rules.
Never changes authentication, ports, REALITY settings, firewall verdicts or IP routes.
"""
from __future__ import print_function
import argparse
import copy
import fcntl
import hashlib
import json
import os
import sqlite3
import subprocess
import sys
import tempfile
import time

ROOT = '/opt/traffic-bot'
PANEL_DB = '/etc/x-ui/x-ui.db'
XRAY = '/usr/local/x-ui/bin/xray-linux-amd64'
LIVE = '/usr/local/x-ui/bin/config.json'
MANIFEST = '/etc/traffic-bot-monitor.json'
BACKUP = ROOT + '/monitor-backup'
PREFIX = '0x7a000000/0xff000000'
UP = 'TBOT_MON_UP'
DOWN = 'TBOT_MON_DOWN'

# Whole domain suffixes, not string contains. Never classify all of Google's
# shared CDNs as YouTube. Rules are intentionally inspectable and extendable.
CATEGORIES = [
    {'name': 'YouTube', 'geosite': 'youtube', 'domains': ['youtube.com', 'youtu.be', 'youtube-nocookie.com', 'googlevideo.com', 'ytimg.com', 'youtubei.googleapis.com', 'youtube.googleapis.com', 'youtubekids.com', 'yt.be', 'withyoutube.com']},
    {'name': 'Netflix', 'domains': ['netflix.com', 'netflix.net', 'nflxvideo.net', 'nflximg.net', 'nflximg.com', 'nflxext.com', 'nflxso.net']},
    {'name': 'OpenAI', 'domains': ['openai.com', 'chatgpt.com', 'oaistatic.com', 'oaiusercontent.com']},
    {'name': 'Telegram', 'domains': ['telegram.org', 't.me', 'telegram.me', 'telegram.dog', 'telesco.pe']},
    {'name': 'Bilibili', 'domains': ['bilibili.com', 'bilibili.tv', 'b23.tv', 'bilivideo.com', 'bilivideo.cn', 'hdslb.com']},
    {'name': 'Other', 'domains': []},
    {'name': 'TikTok', 'domains': ['tiktok.com', 'tiktokv.com', 'tiktokcdn.com', 'tiktokcdn-us.com', 'tiktokcdn-eu.com', 'musical.ly']},
    {'name': 'Douyin', 'domains': ['douyin.com', 'douyincdn.com', 'douyinpic.com', 'douyinvod.com', 'iesdouyin.com', 'amemv.com']},
    {'name': 'Instagram', 'domains': ['instagram.com', 'cdninstagram.com']},
    {'name': 'Facebook', 'domains': ['facebook.com', 'facebook.net', 'fbcdn.net', 'fb.com', 'messenger.com']},
    {'name': 'Steam', 'domains': ['steampowered.com', 'steamcommunity.com', 'steamcontent.com', 'steamstatic.com', 'steamserver.net', 's.team']},
    {'name': 'EpicGames', 'domains': ['epicgames.com', 'epicgames.dev', 'epicgamescdn.com', 'fortnite.com', 'unrealengine.com', 'unrealtournament.com', 'easyanticheat.net', 'easy.ac', 'eac-cdn.com']},
    {'name': 'Roblox', 'domains': ['roblox.com', 'robloxlabs.com', 'rbxcdn.com', 'rbxinfra.net', 'rbx.com', 'rblx.com', 'rblx.co'], 'ips': ['128.116.0.0/17']},
    {'name': 'Leigod', 'domains': ['leigod.com', 'leigod.cn', 'leigodjsq.com', 'ls66.cn', 'nnbooster.com', 'xxghh.biz', 'gangwan.com', 'nn.com', 'waifushangcheng.com']},
    {'name': 'iCloud', 'domains': ['icloud.com', 'icloud.com.cn', 'icloud-content.com', 'me.com']},
    {'name': 'OneDrive', 'domains': ['1drv.com', '1drv.ms', 'onedrive.com', 'onedrive.net', 'onedrive.live.com', 'storage.live.com', 'livefilestore.com']},
    {'name': 'Dropbox', 'domains': ['dropbox.com', 'dropboxapi.com', 'dropboxusercontent.com', 'dropboxstatic.com', 'dropbox-dns.com']},
    {'name': 'MEGA', 'domains': ['mega.nz', 'mega.co.nz', 'mega.io']},
    {'name': 'AppleUpdate', 'domains': ['appldnld.apple.com', 'gdmf.apple.com', 'gdmf-ados.apple.com', 'mesu.apple.com', 'oscdn.apple.com', 'osrecovery.apple.com', 'swcdn.apple.com', 'swdist.apple.com', 'swdownload.apple.com', 'swscan.apple.com', 'updates.cdn-apple.com', 'updates-http.cdn-apple.com']},
    {'name': 'Microsoft', 'domains': ['microsoft.com', 'microsoftonline.com', 'office.com', 'office.net', 'office365.com', 'windows.com', 'windows.net', 'windowsupdate.com', 'update.microsoft.com', 'delivery.mp.microsoft.com', 'dl.delivery.mp.microsoft.com']}
]
# Category IDs are persistent database identities: never reorder this list.
# Domain sources: github.com/v2fly/domain-list-community/tree/master/data
# Roblox IP ownership: rdap.arin.net/registry/ip/128.116.0.0 (Roblox /17).
# Shared cloud IPs and broad UDP port ranges deliberately remain unclassified.

def run(args, quiet=False, cwd=None):
    out = subprocess.check_output(args, stderr=subprocess.STDOUT, cwd=cwd)
    if not quiet and out:
        print(out.decode('utf-8', 'replace').strip())
    return out

def exists(args):
    with open(os.devnull, 'w') as null:
        return subprocess.call(args, stdout=null, stderr=null) == 0

def read_json(path):
    with open(path) as f:
        return json.load(f)

def write_json(path, value):
    parent = os.path.dirname(path)
    fd, temp = tempfile.mkstemp(prefix='.tbot-', dir=parent)
    try:
        with os.fdopen(fd, 'w') as f:
            json.dump(value, f, indent=2, sort_keys=True)
            f.write('\n')
            f.flush()
            os.fsync(f.fileno())
        os.chmod(temp, 0o600)
        os.rename(temp, path)
        if hasattr(os, 'O_DIRECTORY'):
            directory = os.open(parent, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)

def template_value():
    conn = sqlite3.connect(PANEL_DB, timeout=10)
    try:
        row = conn.execute("SELECT value FROM settings WHERE key='xrayTemplateConfig'").fetchone()
        if not row:
            raise RuntimeError('X-UI template not found')
        return row[0]
    finally:
        conn.close()

def set_template(value):
    conn = sqlite3.connect(PANEL_DB, timeout=10)
    try:
        conn.execute("UPDATE settings SET value=? WHERE key='xrayTemplateConfig'", (value,))
        conn.commit()
    finally:
        conn.close()

def digest(s):
    return hashlib.sha256(s.encode('utf-8')).hexdigest()

def build_plan(upgrade=False, reconcile=False):
    live = read_json(LIVE)
    original = template_value()
    template = json.loads(original)
    previous = read_json(MANIFEST) if os.path.exists(MANIFEST) else None
    if previous and not upgrade:
        raise RuntimeError('Monitor already configured; use --restore-firewall or --rollback before reconfiguring')
    if upgrade:
        if not previous or digest(original) != read_json(BACKUP + '/owner.json')['applied_hash']:
            raise RuntimeError('Missing integration or template edited since install; refusing upgrade')
        owned = set('tbot-%d-%d' % ((c['mark'] >> 8) & 0xffff, c['mark'] & 255) for c in previous['counters'])
        template['outbounds'] = [o for o in template.get('outbounds', []) if o.get('tag') not in owned]
        template.setdefault('routing', {})['rules'] = [r for r in template.get('routing', {}).get('rules', []) if r.get('outboundTag') not in owned]
    for binary in (() if upgrade else ('iptables', 'ip6tables')):
        rules = run([binary, '-t', 'mangle', '-S'], quiet=True).decode().splitlines()
        if any(line.startswith('-N TBOT_') for line in rules):
            raise RuntimeError('Reserved accounting chain already exists; refusing to take ownership')
    if any('fwmark' in line for command in (['ip', 'rule', 'show'], ['ip', '-6', 'rule', 'show']) for line in run(command, quiet=True).decode().splitlines()):
        raise RuntimeError('Existing fwmark routing needs manual review before socket accounting')
    outbounds = template.get('outbounds', [])
    if not outbounds or outbounds[0].get('protocol') != 'freedom':
        raise RuntimeError('Default outbound is not direct freedom; manual integration required')
    if outbounds[0].get('streamSettings', {}).get('sockopt', {}).get('mark'):
        raise RuntimeError('Default outbound already uses socket marks; refusing to replace them')
    emails = sorted(set(u.get('email', '') for i in live.get('inbounds', [])
                        for u in i.get('settings', {}).get('clients', []) if u.get('email')))
    if (not emails and not reconcile) or len(emails) > 65535:
        raise RuntimeError('Expected labelled users within the mark ID space')
    if any(len(e) > 254 or any(ord(ch) < 32 for ch in e) for e in emails):
        raise RuntimeError('Invalid user email label')
    if any(o.get('tag', '').startswith('tbot-') for o in outbounds):
        raise RuntimeError('Reserved outbound tag already exists')
    old_uids = {}
    if upgrade:
        for c in previous['counters']:
            uid = (c['mark'] >> 8) & 0xffff
            if c['email'] in old_uids and old_uids[c['email']] != uid:
                raise RuntimeError('Inconsistent existing account mapping')
            old_uids[c['email']] = uid
        if set(old_uids) != set(emails) and not reconcile:
            raise RuntimeError('Account set changed; review account mappings before upgrade')
    next_uid = max(list(old_uids.values()) + [0]) + 1
    if previous:
        next_uid = max(next_uid, previous.get('next_uid', next_uid))
    for email in emails:
        if upgrade and email not in old_uids:
            if next_uid > 65535:
                raise RuntimeError('Accounting account ID space exhausted')
            old_uids[email] = next_uid
            next_uid += 1
    counters, rules, catchall = [], [], []
    for index, email in enumerate(emails, 1):
        uid = old_uids.get(email, index)
        for cid, category in enumerate(CATEGORIES, 1):
            mark = 0x7a000000 | (uid << 8) | cid
            tag = 'tbot-%d-%d' % (uid, cid)
            outbound = copy.deepcopy(outbounds[0])
            outbound['tag'] = tag
            outbound.setdefault('streamSettings', {}).setdefault('sockopt', {})['mark'] = mark
            template['outbounds'].append(outbound)
            rule = {'type': 'field', 'user': [email], 'outboundTag': tag}
            if category['domains']:
                rule['domain'] = ['domain:' + d for d in category['domains']]
                if category.get('geosite'):
                    rule['domain'].append('geosite:' + category['geosite'])
                rules.append(rule)
            if category.get('ips'):
                # Separate rules implement domain OR IP, not Xray's field AND.
                rules.append({'type': 'field', 'user': [email], 'ip': category['ips'], 'outboundTag': tag})
            if not category['domains'] and not category.get('ips'):
                catchall.append(rule)
            counters.append({'mark': mark, 'email': email, 'category': category['name']})
    # Existing API/private-network/BT blocking rules keep their priority.
    template.setdefault('routing', {}).setdefault('rules', []).extend(rules + catchall)
    candidate = copy.deepcopy(live)
    candidate['outbounds'] = template['outbounds']
    candidate['routing'] = template['routing']
    # Validate with the installed core before any modification to panel settings.
    fd, path = tempfile.mkstemp(prefix='tbot-test-', suffix='.json', dir='/tmp')
    try:
        with os.fdopen(fd, 'w') as f:
            json.dump(candidate, f)
        run([XRAY, 'run', '-test', '-config', path], cwd='/usr/local/x-ui')
    finally:
        os.unlink(path)
    if upgrade:
        mapped = dict((c['mark'], (c['email'], c['category'])) for c in counters)
        if any(mapped.get(c['mark']) != (c['email'], c['category']) for c in previous['counters'] if c['email'] in emails):
            raise RuntimeError('Upgrade would change existing counter identities')
    manifest = {'version': 1, 'created': previous['created'] if upgrade else int(time.time()),
                'inbound_sha256': digest(json.dumps(live.get('inbounds', []), sort_keys=True)),
                'categories': CATEGORIES, 'counters': counters}
    manifest['next_uid'] = next_uid if upgrade else len(emails) + 1
    for key in ('categories_updated', 'accounts_updated'):
        if previous and key in previous:
            manifest[key] = previous[key]
    return original, template, manifest

def retiring_manifest(previous, manifest):
    transitional = copy.deepcopy(previous)
    transitional['reconciling'] = True
    active = set(c['email'] for c in manifest['counters'])
    for counter in transitional['counters']:
        counter['retired'] = counter['email'] not in active
    previous_emails = set(c['email'] for c in previous['counters'])
    transitional['pending_emails'] = sorted(active - previous_emails)
    return transitional

def transaction_path():
    return BACKUP + '/transaction.json'

def monitor_sample():
    # The parent holds the shared runtime lock; ordinary Bot samples cannot race.
    run([ROOT + '/traffic-bot', '--monitor-reconcile-once'], cwd=ROOT, quiet=True)

def live_template_matches(value):
    live = read_json(LIVE)
    expected = json.loads(value)
    return all(live.get(key, {}) == expected.get(key, {}) for key in ('outbounds', 'routing'))

def remove_transaction():
    os.unlink(transaction_path())
    if hasattr(os, 'O_DIRECTORY'):
        fd = os.open(BACKUP, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)

def recovery_warning():
    conn = sqlite3.connect(ROOT + '/traffic-bot.db', timeout=10)
    try:
        conn.execute('INSERT OR REPLACE INTO monitor_state(name,value) VALUES(?,?)', ('last_reset', str(int(time.time()))))
        conn.commit()
    finally:
        conn.close()

def recover_transaction():
    if not os.path.exists(transaction_path()):
        return False
    tx = read_json(transaction_path())
    if tx.get('version') != 1 or tx.get('phase') not in ('prepared', 'committed'):
        raise RuntimeError('Unknown monitor transaction; refusing unsafe recovery')
    current = template_value()
    original, value = tx['template'], tx['next_template']
    if digest(current) not in (digest(original), digest(value)):
        raise RuntimeError('Template manually edited during interrupted synchronization; refusing to overwrite')
    if tx['phase'] == 'committed':
        if digest(current) != digest(value):
            raise RuntimeError('Committed template changed; refusing to overwrite')
        firewall(tx['next_manifest'])
        write_json(MANIFEST, tx['next_manifest'])
        write_json(BACKUP + '/owner.json', {'applied_hash': digest(value)})
        if not live_template_matches(value):
            run(['systemctl', 'restart', 'x-ui'])
        prune_retired_counters(tx['manifest'], tx['next_manifest'])
        install_services()
    else:
        previous = copy.deepcopy(tx['manifest'])
        # Never reassign an ID allocated by an interrupted transaction to a
        # different account, even if its old counter rows survived in SQLite.
        previous['next_uid'] = max(previous.get('next_uid', 1), tx['next_manifest'].get('next_uid', 1))
        firewall(previous)
        if current != original:
            set_template(original)
        if not live_template_matches(original):
            run(['systemctl', 'restart', 'x-ui'])
        write_json(MANIFEST, previous)
        write_json(BACKUP + '/owner.json', tx['owner'])
        prune_retired_counters(tx['next_manifest'], previous)
        recovery_warning()
    remove_transaction()
    print('Recovered interrupted monitor synchronization (%s); Bot was not stopped.' % tx['phase'])
    return True

def upgrade_categories(reconcile=False):
    recover_transaction()
    original, template, manifest = build_plan(upgrade=True, reconcile=reconcile)
    previous = read_json(MANIFEST)
    owner = read_json(BACKUP + '/owner.json')
    if json.loads(original) == template and previous['counters'] == manifest['counters']:
        write_json(MANIFEST, manifest)
        install_services()
        print('Account monitoring is already synchronized; no Xray restart needed.')
        return
    if previous['categories'] != manifest['categories']:
        manifest['categories_updated'] = int(time.time())
    if reconcile:
        manifest['accounts_updated'] = int(time.time())
    value = json.dumps(template, separators=(',', ':'))
    tx = {'version': 1, 'phase': 'prepared', 'template': original,
          'manifest': previous, 'owner': owner,
          'next_template': value, 'next_manifest': manifest}
    # One durable journal and one rolling backup, not a growing log history.
    write_json(BACKUP + '/upgrade.json', {'template': original, 'manifest': previous, 'owner': owner})
    write_json(transaction_path(), tx)
    try:
        write_json(MANIFEST, retiring_manifest(previous, manifest))
        monitor_sample()
        firewall(manifest)  # Append missing rules, never flush existing counters.
        set_template(value)
        run(['systemctl', 'restart', 'x-ui'])
        time.sleep(3)
        run(['systemctl', 'is-active', 'x-ui'])
        live = read_json(LIVE)
        if digest(json.dumps(live.get('inbounds', []), sort_keys=True)) != manifest['inbound_sha256']:
            raise RuntimeError('Inbound identity changed during upgrade')
        marks = set(o.get('streamSettings', {}).get('sockopt', {}).get('mark') for o in live.get('outbounds', []))
        if not all(c['mark'] in marks for c in manifest['counters']):
            raise RuntimeError('Expanded outbounds did not become active')
        ports = [i['port'] for i in live.get('inbounds', []) if i.get('tag') == 'api' and i.get('listen') == '127.0.0.1']
        if not ports:
            raise RuntimeError('Local Xray health endpoint not found')
        for inbound in live.get('inbounds', []):
            if inbound.get('settings', {}).get('clients'):
                run([XRAY, 'api', 'inboundusercount', '--server=127.0.0.1:%d' % ports[0], '-tag', inbound['tag']], quiet=True)
        if reconcile:
            # Include old connection teardown bytes before removing their rules.
            monitor_sample()
        write_json(MANIFEST, manifest)
        write_json(BACKUP + '/owner.json', {'applied_hash': digest(value)})
        monitor_sample()
        tx['phase'] = 'committed'
        write_json(transaction_path(), tx)
        if reconcile:
            prune_retired_counters(previous, manifest)
        install_services()
        remove_transaction()
    except Exception:
        recover_transaction()
        raise
    print('Synchronized %d accounts, %d categories; pending bytes and history preserved.' % (len(set(c['email'] for c in manifest['counters'])), len(CATEGORIES)))

def prune_retired_counters(previous, manifest):
    active_uids = set((c['mark'] >> 8) & 0xffff for c in manifest['counters'])
    removed = [c for c in previous['counters'] if (c['mark'] >> 8) & 0xffff not in active_uids]
    for binary in ('iptables', 'ip6tables'):
        base = [binary, '-w', '-t', 'mangle']
        for uid in sorted(set((c['mark'] >> 8) & 0xffff for c in removed)):
            for parent, match, chain in ((UP, 'mark', 'TBOT_U%04x' % uid), (DOWN, 'connmark', 'TBOT_D%04x' % uid)):
                args = ['-m', match, '--mark', hex(0x7a000000 | (uid << 8)) + '/0xffffff00', '-g', chain]
                if exists(base + ['-C', parent] + args):
                    run(base + ['-D', parent] + args)
                if exists(base + ['-S', chain]):
                    run(base + ['-F', chain])
                    run(base + ['-X', chain])
    conn = sqlite3.connect(ROOT + '/traffic-bot.db', timeout=10)
    try:
        for c in removed:
            for family in ('iptables-save', 'ip6tables-save'):
                conn.execute('DELETE FROM monitor_counters WHERE counter_key=?', (family + ':' + str(c['mark']),))
        conn.commit()
    finally:
        conn.close()

def firewall(manifest):
    for binary in ('iptables', 'ip6tables'):
        base = [binary, '-w', '-t', 'mangle']
        for chain in (UP, DOWN):
            if not exists(base + ['-S', chain]):
                run(base + ['-N', chain])
        save = ['-m', 'connmark', '!', '--mark', PREFIX, '-j', 'CONNMARK', '--save-mark']
        if not exists(base + ['-C', UP] + save):
            run(base + ['-I', UP, '1'] + save)
        # Two-level dispatch avoids scanning every device/category per packet.
        for group_mark in sorted(set(c['mark'] & 0xffffff00 for c in manifest['counters'])):
            uid = (group_mark >> 8) & 0xffff
            for parent, match, chain in ((UP, 'mark', 'TBOT_U%04x' % uid), (DOWN, 'connmark', 'TBOT_D%04x' % uid)):
                if not exists(base + ['-S', chain]):
                    run(base + ['-N', chain])
                dispatch = ['-m', match, '--mark', hex(group_mark) + '/0xffffff00', '-g', chain]
                if not exists(base + ['-C', parent] + dispatch):
                    run(base + ['-A', parent] + dispatch)
        for c in manifest['counters']:
            value = hex(c['mark'])
            uid = (c['mark'] >> 8) & 0xffff
            for chain, match in (('TBOT_U%04x' % uid, 'mark'), ('TBOT_D%04x' % uid, 'connmark')):
                args = ['-m', match, '--mark', value, '-j', 'RETURN']
                if not exists(base + ['-C', chain] + args):
                    run(base + ['-A', chain] + args)
        for hook, match, chain in (('OUTPUT', 'mark', UP), ('PREROUTING', 'connmark', DOWN)):
            args = ['-m', match, '--mark', PREFIX, '-j', chain]
            if not exists(base + ['-C', hook] + args):
                run(base + ['-A', hook] + args)

def remove_firewall(manifest):
    for binary in ('iptables', 'ip6tables'):
        base = [binary, '-w', '-t', 'mangle']
        for hook, match, chain in (('OUTPUT', 'mark', UP), ('PREROUTING', 'connmark', DOWN)):
            args = ['-m', match, '--mark', PREFIX, '-j', chain]
            if exists(base + ['-C', hook] + args):
                run(base + ['-D', hook] + args)
        for chain in (UP, DOWN):
            if exists(base + ['-S', chain]):
                run(base + ['-F', chain])
                run(base + ['-X', chain])
        for uid in sorted(set((c['mark'] >> 8) & 0xffff for c in manifest['counters'])):
            for chain in ('TBOT_U%04x' % uid, 'TBOT_D%04x' % uid):
                if exists(base + ['-S', chain]):
                    run(base + ['-F', chain])
                    run(base + ['-X', chain])

def install_services():
    unit = '''[Unit]
Description=Traffic Bot accounting rules (counter-only)
Before=x-ui.service traffic-bot.service
After=network-pre.target
ConditionPathExists=/etc/traffic-bot-monitor.json

[Service]
Type=oneshot
ExecStart=/usr/bin/python /opt/traffic-bot/monitor_setup.py --restore-firewall
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
'''
    with open('/etc/systemd/system/traffic-bot-accounting.service', 'w') as f:
        f.write(unit)
    reconcile = '''[Unit]
Description=Synchronize Traffic Bot account monitoring
After=network.target
ConditionPathExists=/etc/traffic-bot-monitor.json

[Service]
Type=oneshot
WorkingDirectory=/opt/traffic-bot
ExecStart=/usr/bin/python /opt/traffic-bot/monitor_setup.py --reconcile
TimeoutStartSec=180
'''
    with open('/etc/systemd/system/traffic-bot-reconcile.service', 'w') as f:
        f.write(reconcile)
    run(['systemctl', 'daemon-reload'])
    run(['systemctl', 'enable', 'traffic-bot-accounting'])

def apply():
    original, template, manifest = build_plan()
    if os.path.exists(BACKUP):
        raise RuntimeError('Existing monitor-backup must be reviewed before a new integration')
    os.mkdir(BACKUP, 0o700)
    write_json(BACKUP + '/template.json', {'value': original})
    value = json.dumps(template, separators=(',', ':'))
    write_json(BACKUP + '/owner.json', {'applied_hash': digest(value)})
    try:
        firewall(manifest)
        set_template(value)
        write_json(MANIFEST, manifest)
        install_services()
        run(['systemctl', 'restart', 'x-ui'])
        time.sleep(3)
        run(['systemctl', 'is-active', 'x-ui'])
        live = read_json(LIVE)
        if not any(o.get('tag', '').startswith('tbot-') for o in live.get('outbounds', [])):
            raise RuntimeError('X-UI did not apply the new template')
        original_users = sorted(c['email'] for c in manifest['counters'] if c['category'] == 'Other')
        live_users = sorted(u.get('email') for i in live['inbounds'] for u in i.get('settings', {}).get('clients', []))
        if original_users != live_users:
            raise RuntimeError('User set changed during integration')
        if digest(json.dumps(live.get('inbounds', []), sort_keys=True)) != manifest['inbound_sha256']:
            raise RuntimeError('Inbound credentials/settings changed during integration')
        api_ports = [i['port'] for i in live.get('inbounds', []) if i.get('tag') == 'api' and i.get('listen') == '127.0.0.1']
        if not api_ports:
            raise RuntimeError('Local Xray health endpoint not found')
        for i in live.get('inbounds', []):
            if i.get('settings', {}).get('clients'):
                run([XRAY, 'api', 'inboundusercount', '--server=127.0.0.1:%d' % api_ports[0], '-tag', i['tag']], quiet=True)
        print('Accounting installed: %d users, %d categories; original credentials and :443 preserved.' % (len(original_users), len(CATEGORIES)))
    except Exception:
        set_template(original)
        remove_firewall(manifest)
        if os.path.exists(MANIFEST):
            os.rename(MANIFEST, BACKUP + '/failed-manifest.json')
        run(['systemctl', 'restart', 'x-ui'])
        raise

def rollback():
    owner = read_json(BACKUP + '/owner.json')
    if digest(template_value()) != owner['applied_hash']:
        raise RuntimeError('Template changed since install; refusing to overwrite later edits')
    set_template(read_json(BACKUP + '/template.json')['value'])
    remove_firewall(read_json(MANIFEST))
    os.rename(MANIFEST, BACKUP + '/disabled-manifest.json')
    run(['systemctl', 'disable', 'traffic-bot-accounting'])
    run(['systemctl', 'restart', 'x-ui'])
    print('Restored original template. Account credentials and Bot database retained.')

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    group = parser.add_mutually_exclusive_group(required=True)
    for action in ('plan', 'apply', 'rollback', 'restore-firewall', 'plan-upgrade', 'upgrade', 'plan-reconcile', 'reconcile', 'install-services'):
        group.add_argument('--' + action, action='store_true')
    args = parser.parse_args()
    os.umask(0o077)
    # Manual and automatic syncs share a runtime lock; never race template/DB edits.
    lock = None
    if args.apply or args.upgrade or args.reconcile or args.rollback or args.restore_firewall:
        lock = open('/run/traffic-bot-monitor.lock', 'w')
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    if args.plan or args.plan_upgrade or args.plan_reconcile:
        _, _, manifest = build_plan(upgrade=args.plan_upgrade or args.plan_reconcile, reconcile=args.plan_reconcile)
        print('Validated plan: %d counters. Device labels:' % len(manifest['counters']))
        for c in manifest['counters']:
            if c['category'] == 'Other':
                print(c['email'])
    elif args.apply:
        apply()
    elif args.upgrade:
        upgrade_categories()
    elif args.reconcile:
        upgrade_categories(reconcile=True)
    elif args.restore_firewall:
        firewall(read_json(MANIFEST))
    elif args.install_services:
        install_services()
    else:
        recover_transaction()
        rollback()

if __name__ == '__main__':
    try:
        main()
    except Exception as e:
        print('ERROR:', str(e), file=sys.stderr)
        sys.exit(1)
