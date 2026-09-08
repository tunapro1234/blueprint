"""Release-source isolation; no signing keys, network, or production writes."""
import importlib.util
import json
import hashlib
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('publish_release', ROOT / 'scripts/publish-release.py')
publish = importlib.util.module_from_spec(spec)
spec.loader.exec_module(publish)

class ReleaseSourceTest(unittest.TestCase):
    def test_snapshot_excludes_untracked_and_later_edits(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / 'repo'
            root.mkdir()
            def git(*args):
                return subprocess.check_output(['git', *args], cwd=root, text=True).strip()
            git('init', '-q')
            git('config', 'user.email', 'fixture@example.invalid')
            git('config', 'user.name', 'fixture')
            (root / 'main.go').write_text('committed source')
            git('add', 'main.go')
            git('commit', '-qm', 'fixture')
            revision = git('rev-parse', 'HEAD')
            (root / 'injected.go').write_text('must not enter release')
            (root / 'main.go').write_text('concurrent edit')
            snapshot = publish.snapshot_source(root, revision, Path(temp) / 'snapshot')
            self.assertFalse((snapshot / 'injected.go').exists())
            self.assertEqual((snapshot / 'main.go').read_text(), 'committed source')
            self.assertEqual(subprocess.check_output(['git','rev-parse','HEAD'], cwd=snapshot, text=True).strip(), revision)

    def test_publish_zero_does_not_publish(self):
        for flag, expected in [('0', False), ('1', True), ('', False)]:
            result = subprocess.check_output(['make','-n','release','RELEASE_KEY=/fixture', 'PUBLISH='+flag], cwd=ROOT,text=True)
            command = result.splitlines()[-1]
            self.assertEqual('--publish' in command, expected)

    def test_resume_verifies_artifacts_and_repeats_only_promotion(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp)
            target=root/'release'; target.mkdir()
            site=root/'site'; site.mkdir()
            scratch=root/'scratch'; scratch.mkdir()
            key=root/'key.pem'; public=root/'public.pem'
            subprocess.run(['openssl','genpkey','-algorithm','Ed25519','-out',str(key)],check=True,capture_output=True)
            subprocess.run(['openssl','pkey','-in',str(key),'-pubout','-out',str(public)],check=True,capture_output=True)
            names=['bp-linux-amd64']
            for name in names+['install.sh']:
                (target/name).write_text('fixture '+name)
            hashes={name:hashlib.sha256((target/name).read_bytes()).hexdigest() for name in names+['install.sh']}
            (target/'manifest.json').write_text(json.dumps(dict(version='1.2.3',revision='fixture',sha256=hashes)))
            (target/'checksums.txt').write_text('# bp-release 1.2.3\n'+''.join(h+'  '+name+'\n' for name,h in hashes.items()))
            for payload,signature in [('manifest.json','manifest.sig'),('checksums.txt','checksums.sig')]:
                subprocess.run(['openssl','pkeyutl','-sign','-inkey',str(key),'-rawin','-in',str(target/payload),'-out',str(target/signature)],check=True,capture_output=True)
            publish.verify_existing(target,'1.2.3','fixture',public,names)
            before={p.name:p.read_bytes() for p in target.iterdir()}
            for _ in range(2):publish.promote_release(target,site,'1.2.3',names,scratch)
            self.assertEqual((site/'latest.version').read_text(),'1.2.3\n')
            self.assertEqual(before,{p.name:p.read_bytes() for p in target.iterdir()})
            (target/names[0]).write_text('tampered')
            with self.assertRaisesRegex(RuntimeError,'hash mismatch'):
                publish.verify_existing(target,'1.2.3','fixture',public,names)
            with self.assertRaisesRegex(RuntimeError,'another version/commit'):
                publish.verify_existing(target,'1.2.3','other',public,names)
