import datetime as dt
import json
from pathlib import Path
import tempfile
import unittest
from scripts.bp_runtime_usage import inspect, measure


class RuntimeUsageTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='bp-runtime-usage-')
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name) / 'thread.jsonl'
        self.now = dt.datetime.now(dt.timezone.utc)
        self.since = self.now - dt.timedelta(hours=12)

    def write(self, rows):
        self.path.write_text(''.join(json.dumps(row) + '\n' for row in rows))

    def assistant(self, when, tokens, identity='message1'):
        return dict(type='assistant', timestamp=when.isoformat(), message=dict(id=identity, usage=dict(input_tokens=tokens)))

    def test_metadata_touch_does_not_count_old_total(self):
        self.write([self.assistant(self.now - dt.timedelta(days=3), 25_000_000), dict(type='bridge-session')])
        usage = measure(self.path, 'claude', self.since, self.now)
        self.assertEqual(usage['tokens'], 0)

    def test_usage_deduplicates_streaming_messages(self):
        self.write([self.assistant(self.now, 100), self.assistant(self.now, 100), self.assistant(self.now, 150)])
        usage = measure(self.path, 'claude', self.since, self.now)
        self.assertEqual((usage['tokens'], usage['messages']), (150, 1))

    def test_codex_counts_deltas_never_old_cumulative_total(self):
        def event(when, total):
            return dict(type='event_msg', timestamp=when.isoformat(), payload=dict(type='token_count', info=dict(total_token_usage=dict(total_tokens=total))))
        self.write([event(self.since-dt.timedelta(seconds=1), 20_000_000), event(self.now, 20_000_123)])
        self.assertEqual(measure(self.path, 'codex', self.since, self.now)['tokens'], 123)
        self.write([event(self.now, 20_000_123)])
        usage = measure(self.path, 'codex', self.since, self.now)
        self.assertFalse(usage['complete'])
        self.assertEqual(usage['tokens'], 0)

    def test_op_main_name_is_preserved_and_inactive_never_alerts(self):
        self.write([self.assistant(self.now, 25_000_000)])
        activity = dict(state='working', observed_at=self.now.isoformat(), transcript_path=str(self.path), thread_id='thread', binding='claude-session-name')
        row = dict(name='op-main', folder='/srv/outpost', runtime='claude', activity=activity)
        report = dict(schema_version=2, agents=[row])
        for state in ['working', 'idle', 'unknown', 'blocked', 'closed']:
            activity['state'] = state
            item = inspect(report, self.now)[0]
            self.assertEqual(item['agent'], 'op-main')
            self.assertEqual(item['can_alert'], state == 'working')
        activity['state'] = 'working'
        activity['observed_at'] = (self.now-dt.timedelta(minutes=1)).isoformat()
        self.assertFalse(inspect(report, self.now)[0]['can_alert'])
        activity['observed_at'] = self.now.isoformat()
        activity.pop('binding')
        self.assertFalse(inspect(report, self.now)[0]['can_alert'])
        with self.assertRaises(ValueError):
            inspect(dict(agents=[row]), self.now)

    def test_recent_human_input_excludes_earlier_window_usage(self):
        self.write([self.assistant(self.now-dt.timedelta(hours=1), 25_000_000)])
        row = dict(name='op-main', runtime='claude', last_human_age_seconds=60,
                   activity=dict(state='working', observed_at=self.now.isoformat(),
                                 transcript_path=str(self.path), thread_id='thread', binding='claude-session-name'))
        result = inspect(dict(schema_version=2, agents=[row]), self.now)[0]
        self.assertEqual(result['usage']['tokens'], 0)
        self.assertFalse(result['can_alert'])

    def test_watchdog_candidate_consumes_schema_and_dry_run_never_sends(self):
        import contextlib
        import importlib.util
        import io
        import sys
        from unittest import mock
        from scripts import bp_runtime_usage
        spec = importlib.util.spec_from_file_location('bp_watchdog_candidate', Path(__file__).with_name('otonom-yakit-bekcisi.py'))
        module = importlib.util.module_from_spec(spec)
        with mock.patch.dict(sys.modules, {'bp_runtime_usage': bp_runtime_usage}):
            spec.loader.exec_module(module)
        module.DURUM = str(Path(self.tmp.name) / 'missing-state.json')
        self.write([self.assistant(self.now-dt.timedelta(days=2), 25_000_000)])
        row = dict(name='op-main', folder='/srv/outpost', runtime='claude',
                   activity=dict(state='working', observed_at=self.now.isoformat(),
                                 transcript_path=str(self.path), thread_id='thread', binding='claude-session-name'))
        report = dict(schema_version=2, agents=[row])
        out = io.StringIO()
        with mock.patch.object(sys, 'argv', ['watchdog', '--kuru']), mock.patch.object(module.subprocess, 'check_output', return_value=json.dumps(report)), mock.patch.object(module.os, 'system', side_effect=AssertionError('external message')), contextlib.redirect_stdout(out):
            self.assertIsNone(module.main())
        self.assertIn('esik asilmadi', out.getvalue())
        self.assertNotIn('OTONOM YANMA', out.getvalue())
