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
def snapshot_source(root, revision, destination):
    # A separate checkout includes only the committed tree, never untracked Go
    # files or changes arriving while the release is being compiled.
    run('git','clone','--quiet','--shared','--no-checkout',str(root),str(destination))
    run('git','checkout','--quiet','--detach',revision,cwd=destination)
    return destination

def promote_release(target, site, version, names, scratch):
    # A failed legacy/pointer promotion can be repeated without rebuilding or
    # replacing the immutable release directory.
    for name in names+['checksums.txt','install.sh']:
        atomic_copy(target/name,site/name)
    pointer=scratch/'latest.version'
    pointer.write_text(version+'\n');pointer.chmod(0o644)
    atomic_copy(pointer,site/'latest.version')

def verify_existing(target, version, revision, public_path, names):
    manifest=json.loads((target/'manifest.json').read_text())
    if manifest.get('version')!=version or manifest.get('revision')!=revision:
        raise RuntimeError('immutable release belongs to another version/commit; bump version')
    for payload,signature in [('manifest.json','manifest.sig'),('checksums.txt','checksums.sig')]:
        run('openssl','pkeyutl','-verify','-pubin','-inkey',str(public_path),'-rawin','-in',str(target/payload),'-sigfile',str(target/signature))
    hashes=manifest.get('sha256',{})
    if set(hashes)!=set(names+['install.sh']):
        raise RuntimeError('existing release artifact set differs')
    for name,value in hashes.items():
        if digest(target/name)!=value:raise RuntimeError('existing release hash mismatch: '+name)
    checksums='# bp-release '+version+'\n'+''.join(value+'  '+name+'\n' for name,value in hashes.items())
    if (target/'checksums.txt').read_text()!=checksums:
        raise RuntimeError('existing checksum list differs from signed manifest')

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
    names=['bp-linux-amd64','bp-linux-arm64','bp-darwin-amd64','bp-darwin-arm64']
    target=ROOT/'site/releases'/('v'+version)
    changes=subprocess.check_output(['git','diff','HEAD','--name-only'],cwd=ROOT,text=True).splitlines()
    promotion_paths={'site/'+name for name in names+['checksums.txt','install.sh','latest.version']}
    if changes and not (args.publish and target.exists() and set(changes)<=promotion_paths):
        raise RuntimeError('commit tracked changes before building a release')
    revision=subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip()
    dist=ROOT/'dist';dist.mkdir(exist_ok=True)
    stage=pathlib.Path(tempfile.mkdtemp(prefix='release-v'+version+'-',dir=dist))
    if target.exists():
        if not args.publish:raise RuntimeError('immutable release already exists; bump version')
        verify_existing(target,version,revision,ROOT/'internal/release/release.pub',names)
        promote_release(target,ROOT/'site',version,names,stage)
        print('Completed existing release promotion:',target,flush=True)
        return
    source=snapshot_source(ROOT,revision,stage/'source')
    if (source/'internal/release/version.txt').read_text().strip()!=version or (source/'internal/release/release.pub').read_bytes()!=public:
        raise RuntimeError('release inputs changed during snapshot; retry from the intended commit')
    if json.loads((source/'npm/package.json').read_text())['version']!=version or (source/'npm/release.pub').read_bytes()!=public or public.decode().strip() not in (source/'install.sh').read_text():
        raise RuntimeError('committed release version/public key copies differ')
    def build(name):
        _,system,arch=name.split('-')
        run('go','build','-mod=readonly','-buildvcs=true','-trimpath','-o',str(stage/name),'./cmd/bp',cwd=source,env=dict(os.environ,GOOS=system,GOARCH=arch,CGO_ENABLED='0',GOCACHE='/tmp/blueprint-go-cache',GOWORK='off',GOFLAGS='',GOENV='off'))
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:list(pool.map(build,names))
    shutil.copy2(source/'install.sh',stage/'install.sh')
    manifest=dict(version=version,revision=revision,published=datetime.datetime.now(datetime.timezone.utc).isoformat(),sha256={name:digest(stage/name) for name in names+['install.sh']})
    (stage/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
    (stage/'checksums.txt').write_text('# bp-release '+version+'\n'+''.join(value+'  '+name+'\n' for name,value in manifest['sha256'].items()))
    for payload,signature in [('manifest.json','manifest.sig'),('checksums.txt','checksums.sig')]:
        run('openssl','pkeyutl','-sign','-inkey',str(args.key),'-rawin','-in',str(stage/payload),'-out',str(stage/signature))
        run('openssl','pkeyutl','-verify','-pubin','-inkey',str(source/'internal/release/release.pub'),'-rawin','-in',str(stage/payload),'-sigfile',str(stage/signature))
    print('Verified release candidate:',stage,flush=True)
    if not args.publish:return
    target.parent.mkdir(exist_ok=True)
    # Stage under the served filesystem, then expose the complete version at once.
    hidden=pathlib.Path(tempfile.mkdtemp(prefix='.publish-',dir=target.parent))
    for artifact in stage.iterdir():
        if artifact.is_file():shutil.copy2(artifact,hidden/artifact.name)
    hidden.chmod(0o755)
    for p in hidden.iterdir():p.chmod(0o755 if p.name.startswith('bp-') or p.name=='install.sh' else 0o644)
    os.rename(hidden,target)
    promote_release(target,ROOT/'site',version,names,stage)
    print('Published:',target,'revision',revision,flush=True)
if __name__=='__main__':main()
