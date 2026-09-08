#!/usr/bin/env python3
"""Build and sign an immutable native release; --publish switches latest last.

Private signing keys stay outside the checkout. Does not install/restart services.
"""
import argparse, concurrent.futures, datetime, hashlib, json, os, pathlib, re, shutil, subprocess, tempfile
ROOT=pathlib.Path(__file__).resolve().parents[1]
def run(*args,**kwargs):return subprocess.run(args,check=True,**kwargs)
def digest(path):return hashlib.sha256(path.read_bytes()).hexdigest()
def atomic_copy(source,target):
    fd,name=tempfile.mkstemp(prefix='.release-',dir=target.parent)
    with os.fdopen(fd,'wb') as f:f.write(source.read_bytes());f.flush();os.fsync(f.fileno())
    os.chmod(name,source.stat().st_mode&0o777);os.replace(name,target)
def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--key',type=pathlib.Path,required=True)
    parser.add_argument('--publish',action='store_true')
    args=parser.parse_args()
    if args.key.stat().st_mode&0o077:raise RuntimeError('signing key must be private (0600)')
    version=(ROOT/'internal/release/version.txt').read_text().strip()
    if not re.fullmatch(r'\d+\.\d+\.\d+',version):raise RuntimeError('invalid version')
    public=(ROOT/'internal/release/release.pub').read_bytes()
    if public!=(ROOT/'npm/release.pub').read_bytes() or public.decode().strip() not in (ROOT/'install.sh').read_text():raise RuntimeError('public key copies differ')
    if json.loads((ROOT/'npm/package.json').read_text())['version']!=version:raise RuntimeError('npm/native version mismatch')
    changes=subprocess.check_output(['git','status','--porcelain','--untracked-files=no'],cwd=ROOT,text=True)
    if changes.strip():raise RuntimeError('commit tracked changes before building a release')
    revision=subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip()
    target=ROOT/'site/releases'/('v'+version)
    if target.exists():raise RuntimeError('immutable release already exists; bump version')
    dist=ROOT/'dist';dist.mkdir(exist_ok=True)
    stage=pathlib.Path(tempfile.mkdtemp(prefix='release-v'+version+'-',dir=dist))
    names=['bp-linux-amd64','bp-linux-arm64','bp-darwin-amd64','bp-darwin-arm64']
    def build(name):
        _,system,arch=name.split('-')
        run('go','build','-trimpath','-o',str(stage/name),'./cmd/bp',cwd=ROOT,env=dict(os.environ,GOOS=system,GOARCH=arch,CGO_ENABLED='0',GOCACHE='/tmp/blueprint-go-cache'))
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:list(pool.map(build,names))
    shutil.copy2(ROOT/'install.sh',stage/'install.sh')
    manifest=dict(version=version,revision=revision,published=datetime.datetime.now(datetime.timezone.utc).isoformat(),sha256={name:digest(stage/name) for name in names+['install.sh']})
    (stage/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
    (stage/'checksums.txt').write_text('# bp-release '+version+'\n'+''.join(value+'  '+name+'\n' for name,value in manifest['sha256'].items()))
    for payload,signature in [('manifest.json','manifest.sig'),('checksums.txt','checksums.sig')]:
        run('openssl','pkeyutl','-sign','-inkey',str(args.key),'-rawin','-in',str(stage/payload),'-out',str(stage/signature))
        run('openssl','pkeyutl','-verify','-pubin','-inkey',str(ROOT/'internal/release/release.pub'),'-rawin','-in',str(stage/payload),'-sigfile',str(stage/signature))
    print('Verified release candidate:',stage,flush=True)
    if not args.publish:return
    target.parent.mkdir(exist_ok=True)
    # Stage under the served filesystem, then expose the complete version at once.
    hidden=pathlib.Path(tempfile.mkdtemp(prefix='.publish-',dir=target.parent))
    for source in stage.iterdir():shutil.copy2(source,hidden/source.name)
    hidden.chmod(0o755)
    for p in hidden.iterdir():p.chmod(0o755 if p.name.startswith('bp-') or p.name=='install.sh' else 0o644)
    os.rename(hidden,target)
    # Legacy URLs remain compatible with old installers; new ones use the version.
    for name in names+['checksums.txt','install.sh']:atomic_copy(target/name,ROOT/'site'/name)
    pointer=stage/'latest.version';pointer.write_text(version+'\n');pointer.chmod(0o644)
    atomic_copy(pointer,ROOT/'site/latest.version')
    print('Published:',target,'revision',revision,flush=True)
if __name__=='__main__':main()
