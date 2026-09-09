import os
from pathlib import Path
import subprocess
import tempfile
import unittest

HELPER = Path(__file__).with_name('health_watch_delivery.sh')

class HealthDeliveryTest(unittest.TestCase):
    def test_delivery_outcomes_preserve_evidence_and_do_not_retry(self):
        for output, rc, expected in [
            ('RESULT=delivered CHANNEL=q1', 0, 0),
            ('RESULT=queued CHANNEL=q2', 0, 0),
            ('TESLIMAT BELIRSIZ\nRESULT=unverified CHANNEL=q3', 1, 0),
            ('ERROR: sender identity unavailable', 1, 1),
            ('ERROR: storage unavailable', 1, 1),
        ]:
            with self.subTest(output=output), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                (root/'bp').write_text('#!/bin/bash\nprintf "call\\n" >> "$CALLS"\nprintf "%s\\n" "$OUTPUT"\nexit "$EXIT_CODE"\n')
                (root/'bp').chmod(0o755)
                env = dict(os.environ, PATH=tmp+':'+os.environ['PATH'], OUTPUT=output,
                           EXIT_CODE=str(rc), CALLS=str(root/'calls'))
                result = subprocess.run(['bash', '-c', 'source "$1"; bp_health_send target "alarm" "$2"',
                                         'test', str(HELPER), str(root/'log')], env=env)
                self.assertEqual(result.returncode, expected)
                self.assertIn(output, (root/'log').read_text())
                self.assertEqual((root/'calls').read_text(), 'call\n')

if __name__ == '__main__':
    unittest.main()
