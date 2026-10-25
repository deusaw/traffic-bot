import copy
import json
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


if __name__ == '__main__':
    unittest.main()
