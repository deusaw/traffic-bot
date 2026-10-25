#!/usr/bin/env python
"""Optional X-UI accounting integration. Python 2.7/3, standard library only.

--plan: read-only validation; --apply: preserve identity, add socket marks and
counter-only rules; --rollback: restore only our template change and rules.
Never changes authentication, ports, REALITY settings, firewall verdicts or IP routes.
"""
from __future__ import print_function
import argparse
import copy
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
    {'name': 'Other', 'domains': []}
]

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

def build_plan():
    live = read_json(LIVE)
    original = template_value()
    template = json.loads(original)
    previous = read_json(MANIFEST) if os.path.exists(MANIFEST) else None
    if previous:
        raise RuntimeError('Monitor already configured; use --restore-firewall or --rollback before reconfiguring')
    for binary in ('iptables', 'ip6tables'):
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
    if not emails or len(emails) > 200:
        raise RuntimeError('Expected 1..200 uniquely labelled users')
    if any(len(e) > 254 or any(ord(ch) < 32 for ch in e) for e in emails):
        raise RuntimeError('Invalid user email label')
    if any(o.get('tag', '').startswith('tbot-') for o in outbounds):
        raise RuntimeError('Reserved outbound tag already exists')
    counters, rules, catchall = [], [], []
    for uid, email in enumerate(emails, 1):
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
            else:
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
    manifest = {'version': 1, 'created': int(time.time()),
                'inbound_sha256': digest(json.dumps(live.get('inbounds', []), sort_keys=True)),
                'categories': CATEGORIES, 'counters': counters}
    return original, template, manifest

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
    for action in ('plan', 'apply', 'rollback', 'restore-firewall'):
        group.add_argument('--' + action, action='store_true')
    args = parser.parse_args()
    os.umask(0o077)
    if args.plan:
        _, _, manifest = build_plan()
        print('Validated plan: %d counters. Device labels:' % len(manifest['counters']))
        for c in manifest['counters']:
            if c['category'] == 'Other':
                print(c['email'])
    elif args.apply:
        apply()
    elif args.restore_firewall:
        firewall(read_json(MANIFEST))
    else:
        rollback()

if __name__ == '__main__':
    try:
        main()
    except Exception as e:
        print('ERROR:', str(e), file=sys.stderr)
        sys.exit(1)
