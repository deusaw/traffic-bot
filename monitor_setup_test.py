import copy
import json
import os
import sqlite3
import tempfile
import unittest
from unittest.mock import patch
import monitor_setup as setup


class SetupSafetyTests(unittest.TestCase):
    def test_plan_preserves_identity_policy_and_does_not_enable_logs(self):
        live = {'inbounds': [{'port': 443, 'protocol': 'vless',
                             'settings': {'clients': [{'id': 'synthetic-identity', 'email': 'HOME'}]},
                             'streamSettings': {'security': 'reality', 'realitySettings': {'privateKey': 'test-only'}}}],
                'outbounds': [{'protocol': 'freedom'}, {'tag': 'blocked', 'protocol': 'blackhole'}],
                'routing': {'rules': [{'ip': ['geoip:private'], 'outboundTag': 'blocked'}]}}
        original = copy.deepcopy(live)
        captured = {}

        def fake_run(args, **kw):
            if '-test' in args:
                with open(args[-1]) as f:
                    captured['candidate'] = json.load(f)
            return b''

        with patch.object(setup, 'read_json', return_value=live), patch.object(setup, 'template_value', return_value=json.dumps({'outbounds': live['outbounds'], 'routing': live['routing']})), patch.object(setup.os.path, 'exists', return_value=False), patch.object(setup, 'run', side_effect=fake_run):
            _, template, manifest = setup.build_plan()
        self.assertEqual(live, original)
        self.assertEqual(captured['candidate']['inbounds'], original['inbounds'])
        self.assertEqual(template['routing']['rules'][0], original['routing']['rules'][0])
        self.assertNotIn('log', template)
        self.assertNotIn('access_log', manifest)
        self.assertEqual(len(manifest['counters']), len(setup.CATEGORIES))
        self.assertEqual(len(set(x['mark'] for x in manifest['counters'])), len(setup.CATEGORIES))
        for outbound in template['outbounds'][2:]:
            self.assertEqual(outbound['protocol'], 'freedom')
            self.assertEqual(outbound['streamSettings']['sockopt']['mark'] & 0xff000000, 0x7a000000)

    def test_firewall_only_counts_and_uses_two_level_dispatch(self):
        commands = []
        manifest = {'counters': [{'mark': 0x7a000101, 'email': 'HOME', 'category': 'YouTube'}]}
        with patch.object(setup, 'exists', return_value=False), patch.object(setup, 'run', side_effect=lambda args: commands.append(args)):
            setup.firewall(manifest)
        targets = [args[args.index('-j') + 1] for args in commands if '-j' in args]
        self.assertNotIn('DROP', targets)
        self.assertNotIn('ACCEPT', targets)
        self.assertTrue(any('-g' in args for args in commands))
        self.assertTrue(any('CONNMARK' in args for args in commands))
        self.assertFalse(any('-F' in args or '-P' in args for args in commands))

    def test_upgrade_preserves_old_marks_and_rules_and_separates_ip_matching(self):
        live = {'inbounds': [{'settings': {'clients': [{'email': 'HOME'}]}}]}
        original_rule = {'ip': ['geoip:private'], 'outboundTag': 'blocked'}
        template = {'dns': {'servers': ['localhost']},
                    'outbounds': [{'protocol': 'freedom', 'settings': {'domainStrategy': 'UseIP'}}],
                    'routing': {'rules': [original_rule]}}
        # Non-default uid proves an upgrade does not renumber existing devices.
        old = {'created': 100, 'counters': []}
        for cid, category in enumerate(setup.CATEGORIES[:6], 1):
            tag = 'tbot-9-%d' % cid
            old['counters'].append({'mark': 0x7a000900 | cid, 'email': 'HOME', 'category': category['name']})
            template['outbounds'].append({'protocol': 'freedom', 'tag': tag})
            template['routing']['rules'].append({'user': ['HOME'], 'outboundTag': tag})
        value = json.dumps(template)

        def read(path):
            return {setup.LIVE: live, setup.MANIFEST: old,
                    setup.BACKUP + '/owner.json': {'applied_hash': setup.digest(value)}}[path]

        with patch.object(setup, 'read_json', side_effect=read), patch.object(setup, 'template_value', return_value=value), patch.object(setup.os.path, 'exists', return_value=True), patch.object(setup, 'run', return_value=b''):
            _, result, manifest = setup.build_plan(upgrade=True)
        self.assertEqual(manifest['created'], 100)
        self.assertEqual(manifest['counters'][:6], old['counters'])
        self.assertEqual(len(manifest['counters']), 20)
        self.assertEqual(result['dns'], template['dns'])
        rules = result['routing']['rules']
        self.assertEqual(rules[0], original_rule)
        self.assertEqual(rules[-1]['outboundTag'], 'tbot-9-6')
        roblox = [r for r in rules if r.get('outboundTag') == 'tbot-9-13']
        self.assertEqual(len(roblox), 2)
        self.assertIn('domain', roblox[0])
        self.assertNotIn('ip', roblox[0])
        self.assertEqual(roblox[1]['ip'], ['128.116.0.0/17'])
        self.assertNotIn('domain', roblox[1])
        self.assertEqual(len(result['outbounds']), 21)
        self.assertLess(next(i for i, r in enumerate(rules) if r.get('outboundTag') == 'tbot-9-16'), next(i for i, r in enumerate(rules) if r.get('outboundTag') == 'tbot-9-20'))

    def test_upgrade_rejects_unowned_template_edits(self):
        with patch.object(setup, 'read_json', side_effect=[{}, {'counters': []}, {'applied_hash': 'different'}]), patch.object(setup, 'template_value', return_value='{}'), patch.object(setup.os.path, 'exists', return_value=True):
            with self.assertRaisesRegex(RuntimeError, 'template edited'):
                setup.build_plan(upgrade=True)

    def test_reconciliation_removes_only_deleted_account_and_keeps_survivor_ids(self):
        live = {'inbounds': [{'settings': {'clients': [{'email': 'HOME'}]}}]}
        old = {'created': 100, 'categories_updated': 200, 'next_uid': 10,
               'counters': [{'mark': 0x7a000101, 'email': 'DELETED', 'category': 'YouTube'},
                            {'mark': 0x7a000901, 'email': 'HOME', 'category': 'YouTube'}]}
        value = json.dumps({'outbounds': [{'protocol': 'freedom'}, {'tag': 'tbot-1-1'}, {'tag': 'tbot-9-1'}],
                            'routing': {'rules': [{'outboundTag': 'tbot-1-1'}, {'outboundTag': 'tbot-9-1'}]}})
        values = {setup.LIVE: live, setup.MANIFEST: old,
                  setup.BACKUP + '/owner.json': {'applied_hash': setup.digest(value)}}
        with patch.object(setup, 'read_json', side_effect=values.__getitem__), patch.object(setup, 'template_value', return_value=value), patch.object(setup.os.path, 'exists', return_value=True), patch.object(setup, 'run', return_value=b''):
            with self.assertRaisesRegex(RuntimeError, 'Account set changed'):
                setup.build_plan(upgrade=True)
            _, template, manifest = setup.build_plan(upgrade=True, reconcile=True)
        self.assertEqual(set(c['email'] for c in manifest['counters']), {'HOME'})
        self.assertTrue(all((c['mark'] >> 8) & 0xffff == 9 for c in manifest['counters']))
        self.assertEqual(manifest['next_uid'], 10)
        self.assertEqual(manifest['categories_updated'], 200)
        self.assertFalse(any(o.get('tag', '').startswith('tbot-1-') for o in template['outbounds']))
        transition = setup.retiring_manifest(old, manifest)
        self.assertTrue(transition['counters'][0]['retired'])
        self.assertFalse(transition['counters'][1]['retired'])
        self.assertNotIn('retired', old['counters'][0])
        # A mixed add/remove operation allocates above the old high-water mark.
        live['inbounds'][0]['settings']['clients'].append({'email': 'NEW'})
        with patch.object(setup, 'read_json', side_effect=values.__getitem__), patch.object(setup, 'template_value', return_value=value), patch.object(setup.os.path, 'exists', return_value=True), patch.object(setup, 'run', return_value=b''):
            _, _, added = setup.build_plan(upgrade=True, reconcile=True)
        self.assertEqual(set((c['mark'] >> 8) & 0xffff for c in added['counters'] if c['email'] == 'NEW'), {10})
        self.assertEqual(added['next_uid'], 11)
        self.assertEqual(setup.retiring_manifest(old, added)['pending_emails'], ['NEW'])

    def test_retired_cleanup_keeps_active_rules_and_history(self):
        removed = {'mark': 0x7a000101, 'email': 'REMOVED'}
        active = {'mark': 0x7a000901, 'email': 'HOME'}
        commands = []
        with tempfile.TemporaryDirectory() as root:
            conn = sqlite3.connect(os.path.join(root, 'traffic-bot.db'))
            conn.execute('create table monitor_counters (counter_key text primary key, up integer, down integer)')
            conn.execute('create table monitor_usage (email text, down integer)')
            conn.execute('insert into monitor_usage values (?,?)', ('REMOVED', 800000000))
            for c in (removed, active):
                conn.execute('insert into monitor_counters values (?,1,2)', ('iptables-save:' + str(c['mark']),))
            conn.commit()
            with patch.object(setup, 'ROOT', root), patch.object(setup, 'exists', return_value=True), patch.object(setup, 'run', side_effect=lambda args: commands.append(args)):
                setup.prune_retired_counters({'counters': [removed, active]}, {'counters': [active]})
            self.assertEqual(conn.execute('select * from monitor_usage').fetchall(), [('REMOVED', 800000000)])
            self.assertEqual(conn.execute('select counter_key from monitor_counters').fetchall(), [('iptables-save:' + str(active['mark']),)])
            conn.close()
        self.assertTrue(any('TBOT_U0001' in cmd for cmd in commands))
        self.assertFalse(any('TBOT_U0009' in cmd or 'TBOT_D0009' in cmd for cmd in commands))

    def test_failed_final_sample_restores_manifest_and_restarts_bot(self):
        old = {'counters': [{'email': 'DELETED'}]}
        new = {'counters': [{'email': 'HOME'}]}
        owner = {'applied_hash': 'original'}
        writes, commands = [], []
        def run(args, **kw):
            commands.append(args)
            if '--monitor-once' in args:
                raise RuntimeError('sample failed')
            return b''
        with patch.object(setup, 'build_plan', return_value=('original', {}, new)), patch.object(setup, 'read_json', side_effect=[old, owner]), patch.object(setup, 'write_json', side_effect=lambda path, data: writes.append((path, copy.deepcopy(data)))), patch.object(setup, 'set_template') as set_template, patch.object(setup, 'firewall'), patch.object(setup, 'exists', return_value=True), patch.object(setup, 'run', side_effect=run):
            with self.assertRaisesRegex(RuntimeError, 'sample failed'):
                setup.upgrade_categories(reconcile=True)
        self.assertEqual([value for path, value in writes if path == setup.MANIFEST][-1], old)
        set_template.assert_called_once_with('original')
        self.assertEqual(commands[-1], ['systemctl', 'start', 'traffic-bot'])


if __name__ == '__main__':
    unittest.main()
