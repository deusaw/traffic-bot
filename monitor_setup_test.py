import copy
import json
import os
import signal
import shutil
import sqlite3
import subprocess
import sys
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


class PowerLoss(BaseException):
    pass


class TransactionRecoveryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = self.temp.name
        for key, value in {'ROOT': self.root, 'BACKUP': self.root + '/backup',
                           'MANIFEST': self.root + '/manifest.json', 'LIVE': self.root + '/live.json'}.items():
            p = patch.object(setup, key, value)
            p.start()
            self.addCleanup(p.stop)
        os.mkdir(setup.BACKUP)
        self.old = {'version': 1, 'created': 100, 'next_uid': 7, 'categories': [],
                    'counters': [{'mark': 0x7a000101, 'email': 'DELETED', 'category': 'YouTube'},
                                 {'mark': 0x7a000201, 'email': 'HOME', 'category': 'YouTube'}]}
        self.new = copy.deepcopy(self.old)
        self.new['next_uid'] = 8
        self.new['counters'] = [self.old['counters'][1], {'mark': 0x7a000701, 'email': 'NEW', 'category': 'YouTube'}]
        self.users = [{'settings': {'clients': [{'email': 'HOME'}, {'email': 'NEW'}]}, 'tag': 'inbound-443'},
                      {'listen': '127.0.0.1', 'port': 62789, 'tag': 'api'}]
        self.new['inbound_sha256'] = setup.digest(json.dumps(self.users, sort_keys=True))
        self.original = json.dumps({'outbounds': [{'protocol': 'freedom'}, {'tag': 'tbot-1-1'}, {'tag': 'tbot-2-1'}], 'routing': {}})
        self.target = {'outbounds': [{'protocol': 'freedom'},
                                    {'tag': 'tbot-2-1', 'streamSettings': {'sockopt': {'mark': 0x7a000201}}},
                                    {'tag': 'tbot-7-1', 'streamSettings': {'sockopt': {'mark': 0x7a000701}}}], 'routing': {}}
        self.panel_value = self.original
        self.owner = {'applied_hash': setup.digest(self.original)}
        setup.write_json(setup.MANIFEST, self.old)
        setup.write_json(setup.BACKUP + '/owner.json', self.owner)
        self.write_live()
        db = sqlite3.connect(self.root + '/traffic-bot.db')
        db.executescript('CREATE TABLE monitor_counters(counter_key TEXT PRIMARY KEY,up INTEGER,down INTEGER); CREATE TABLE monitor_usage(email TEXT,down INTEGER); CREATE TABLE monitor_state(name TEXT PRIMARY KEY,value TEXT);')
        db.execute('INSERT INTO monitor_usage VALUES (?,?)', ('DELETED', 800000000))
        db.execute('INSERT INTO monitor_counters VALUES (?,1,2)', ('iptables-save:' + str(0x7a000201),))
        db.commit()
        db.close()
        self.commands = []
        patches = [patch.object(setup, 'template_value', side_effect=lambda: self.panel_value),
                   patch.object(setup, 'set_template', side_effect=self.set_panel),
                   patch.object(setup, 'build_plan', side_effect=lambda **kw: (self.panel_value, copy.deepcopy(self.target), copy.deepcopy(self.new))),
                   patch.object(setup, 'run', side_effect=self.fake_run),
                   patch.object(setup, 'exists', return_value=False),
                   patch.object(setup, 'firewall'), patch.object(setup, 'install_services'), patch.object(setup.time, 'sleep')]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)

    def set_panel(self, value):
        self.panel_value = value

    def write_live(self):
        live = json.loads(self.panel_value)
        live['inbounds'] = self.users
        setup.write_json(setup.LIVE, live)

    def fake_run(self, args, **kwargs):
        self.commands.append(args)
        if args == ['systemctl', 'restart', 'x-ui']:
            self.write_live()
        return b''

    def assert_history_and_bot_preserved(self):
        self.assertFalse(any(cmd[:3] in (['systemctl', 'stop', 'traffic-bot'], ['systemctl', 'restart', 'traffic-bot']) for cmd in self.commands))
        con = sqlite3.connect(self.root + '/traffic-bot.db')
        self.assertEqual(con.execute('SELECT * FROM monitor_usage').fetchall(), [('DELETED', 800000000)])
        con.close()
        self.assertEqual(setup.read_json(setup.LIVE)['inbounds'], self.users)

    def test_failed_sample_rolls_back_without_stopping_bot(self):
        with patch.object(setup, 'monitor_sample', side_effect=RuntimeError('sample failed')):
            with self.assertRaisesRegex(RuntimeError, 'sample failed'):
                setup.upgrade_categories(reconcile=True)
        restored = setup.read_json(setup.MANIFEST)
        self.assertEqual(restored['counters'], self.old['counters'])
        self.assertEqual(restored['next_uid'], 8)
        self.assertFalse(os.path.exists(setup.transaction_path()))
        self.assert_history_and_bot_preserved()

    def test_process_loss_after_template_change_recovers_from_durable_journal(self):
        with patch.object(setup, 'monitor_sample', side_effect=[None, PowerLoss()]):
            with self.assertRaises(PowerLoss):
                setup.upgrade_categories(reconcile=True)
        self.assertEqual(setup.read_json(setup.transaction_path())['phase'], 'prepared')
        self.assertTrue(setup.read_json(setup.MANIFEST)['reconciling'])
        self.assertNotEqual(self.panel_value, self.original)
        setup.recover_transaction()
        self.assertEqual(self.panel_value, self.original)
        self.assertFalse(os.path.exists(setup.transaction_path()))
        self.assertEqual(setup.read_json(setup.MANIFEST)['next_uid'], 8)
        self.assert_history_and_bot_preserved()

    def test_committed_cleanup_is_recovered_idempotently_without_rollback(self):
        with patch.object(setup, 'prune_retired_counters', side_effect=PowerLoss()):
            with self.assertRaises(PowerLoss):
                setup.upgrade_categories(reconcile=True)
        self.assertEqual(setup.read_json(setup.transaction_path())['phase'], 'committed')
        self.assertTrue(setup.recover_transaction())
        self.assertFalse(setup.recover_transaction())
        self.assertEqual(json.loads(self.panel_value), self.target)
        self.assertEqual(setup.read_json(setup.MANIFEST)['counters'], self.new['counters'])
        self.assert_history_and_bot_preserved()

    def test_recovery_never_overwrites_manual_template_changes(self):
        with patch.object(setup, 'monitor_sample', side_effect=PowerLoss()):
            with self.assertRaises(PowerLoss):
                setup.upgrade_categories(reconcile=True)
        self.panel_value = '{"manual":true}'
        with self.assertRaisesRegex(RuntimeError, 'manually edited'):
            setup.recover_transaction()
        self.assertEqual(self.panel_value, '{"manual":true}')
        self.assertTrue(os.path.exists(setup.transaction_path()))

    def test_noop_reconcile_does_not_restart_xray(self):
        self.target = json.loads(self.original)
        self.new = copy.deepcopy(self.old)
        with patch.object(setup, 'monitor_sample') as sample:
            setup.upgrade_categories(reconcile=True)
        sample.assert_not_called()
        self.assertFalse(any(cmd[:2] == ['systemctl', 'restart'] for cmd in self.commands))

    @unittest.skipUnless(hasattr(os, 'fork'), 'requires POSIX process signals')
    def test_sigkill_leaves_a_recoverable_journal_and_never_stops_bot(self):
        pid = os.fork()
        if pid == 0:
            with patch.object(setup, 'monitor_sample', side_effect=lambda: os.kill(os.getpid(), signal.SIGKILL)):
                setup.upgrade_categories(reconcile=True)
            os._exit(3)
        _, status = os.waitpid(pid, 0)
        self.assertTrue(os.WIFSIGNALED(status))
        self.assertEqual(os.WTERMSIG(status), signal.SIGKILL)
        self.assertEqual(setup.read_json(setup.transaction_path())['phase'], 'prepared')
        self.assertTrue(setup.recover_transaction())
        self.assertFalse(os.path.exists(setup.transaction_path()))
        self.assert_history_and_bot_preserved()


class UpdateSafetyTests(unittest.TestCase):
    def exercise_failure(self, build_fails):
        with tempfile.TemporaryDirectory() as root:
            live, source, commands = [os.path.join(root, x) for x in ('live', 'source', 'bin')]
            for path in (live, source, commands, live + '/.git'):
                os.mkdir(path)
            for name, content in {'monitor_setup.py': 'previous-helper', 'traffic-bot': 'previous-binary', 'go.sum': 'identical-sum'}.items():
                with open(live + '/' + name, 'w') as f:
                    f.write(content)
            for name, content in {'monitor_setup.py': 'next-helper', 'go.sum': 'identical-sum', 'go.mod': 'module synthetic'}.items():
                with open(source + '/' + name, 'w') as f:
                    f.write(content)
            stubs = {
                'git': '''#!/bin/sh
case "$1" in
 diff|fetch) exit 0;;
 ls-files) exit 1;;
 show) printf 'identical-sum';;
 archive) tar -cf - -C "$TEST_SOURCE" .;;
 merge) cp "$TEST_SOURCE/monitor_setup.py" "$TEST_LIVE/monitor_setup.py"; cp "$TEST_SOURCE/go.sum" "$TEST_LIVE/go.sum";;
 *) exit 2;;
esac
''',
                'go': '''#!/bin/sh
if [ "$1" = test ]; then [ "$TEST_FAIL_BUILD" != 1 ]; exit $?; fi
if [ "$1" = build ]; then printf next-binary > "$3"; fi
''',
                'systemctl': '#!/bin/sh\n[ "$1" != is-active ]\n',
                'id': '#!/bin/sh\nexit 1\n',
                'sleep': '#!/bin/sh\nexit 0\n',
            }
            if not shutil.which('flock'):
                # macOS has the same flock syscall but no util-linux executable.
                # These failure tests need an uncontended inherited-fd lock only.
                stubs['flock'] = '#!' + sys.executable + '\nimport fcntl,sys\nfcntl.flock(int(sys.argv[-1]),fcntl.LOCK_EX)\n'
            for name, content in stubs.items():
                with open(commands + '/' + name, 'w') as f:
                    f.write(content)
                os.chmod(commands + '/' + name, 0o700)
            with open(os.path.join(os.path.dirname(__file__), 'update.sh')) as f:
                script = f.read().replace('/opt/traffic-bot', live).replace('/run/traffic-bot-monitor.lock', root + '/lock')
            path = root + '/update.sh'
            with open(path, 'w') as f:
                f.write(script)
            env = dict(os.environ, PATH=commands + ':' + os.environ['PATH'], TEST_SOURCE=source, TEST_LIVE=live, TEST_FAIL_BUILD='1' if build_fails else '0')
            result = subprocess.run(['bash', path], env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            self.assertNotEqual(result.returncode, 0, result.stdout)
            for name, expected in {'monitor_setup.py': 'previous-helper', 'traffic-bot': 'previous-binary', 'go.sum': 'identical-sum'}.items():
                with open(live + '/' + name) as f:
                    self.assertEqual(f.read(), expected, result.stdout)
            self.assertFalse(any(name.startswith('.update.') for name in os.listdir(live)))

    def test_failed_build_does_not_replace_helper_or_delete_generated_sum(self):
        self.exercise_failure(True)

    def test_failed_health_check_restores_compatible_helper_and_binary(self):
        self.exercise_failure(False)


if __name__ == '__main__':
    unittest.main()
