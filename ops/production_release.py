#!/usr/bin/env python3
"""Root-owned release controller. SSH accepts only deploy/status + a full SHA.

No credentials or database copies leave the VPS. Deploy workers survive SSH
disconnects. Installation and recovery are described in docs/production-deployment.md.
"""
import contextlib
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import time
import traceback
import urllib.request

ROOT = Path('/var/lib/puntazo-production-release')
REPOSITORY = 'https://github.com/am-p/app-loyalty.git'
APP = '0yyhsbzttzprrrukoccrwfp6'
DB = 'cyteflibvyp3bigscehixwav'
TOKEN = Path('/root/.coolify-api-token-puntazo-root')
PUBLIC = 'https://api.puntazo.pro'
SHA = re.compile(r'[0-9a-f]{40}')
TERMINAL = {'finished', 'failed', 'cancelled'}
# Apple identity (0044) and Apple billing (0045) have independent promotion
# paths. Either can be absent from a source branch until its SQL is integrated.
RESERVED_MIGRATION_VERSIONS = {'0044', '0045'}

# Existing main/testing SQL must remain byte-identical. These exact expansions
# were reviewed during PR reconciliation; unknown migrations still need a marker.
REVIEWED_EXPAND_MIGRATIONS = {
    '0036_web_push.up.sql': '65731e140c9543e45ef2973d23361ff1ce9fdd5cb45df709090db426a45703f7',
    '0037_movement_reversals.up.sql': '8ed94b8d20aa7a360ba381ae1fd9f3621c2a496d45767322d951564f527b2507',
    '0038_benefit_expiration.up.sql': '709026f7e5ae609c639d57ca62a5ab0b116fa825b01951ec5cc6c19add3552e8',
    '0039_card_templates.up.sql': '8cf8ba6c28d8f8dd41e73d217007d681ed0613971390730f28856af1b364ec6a',
    '0040_store_favorites.up.sql': 'e65f3e2988e6c0b582eb3df3828b0f5f027714187b6df6f561b75dd31e53bd42',
    '0041_branch_pdf_menus.up.sql': 'c4b15a70312b10e4b9856e78f05a42ffdd969274cd2f91bd5878236608de0663',
    '0042_branch_pdf_30mib.up.sql': 'a4da4fd212f2a23c3020f6daebc7e7d18a0bdec0e00a8420ce0c31c240ee2db9',
    '0043_email_verification_codes.up.sql': 'e313e80e80c0b5f5b8395b8ef4d08b24ee2b7b3fa21ef74b51d87b0a840f8ec2',
}


class ReleaseError(Exception):
    """Only these fixed diagnostic messages may be returned to SSH clients."""


def require(condition, message):
    if not condition:
        raise ReleaseError(message)


def atomic_json(path, value):
    temporary = path.with_suffix('.tmp')
    temporary.write_text(json.dumps(value, indent=2))
    temporary.chmod(0o600)
    temporary.replace(path)


def command(text):
    match = re.fullmatch(r'(deploy|status) ([0-9a-f]{40})', text)
    require(match is not None, 'Only deploy/status followed by a full lowercase SHA is permitted')
    return match.groups()


def migration_sequence(versions):
    if not versions:
        return False
    seen = set(versions)
    return list(versions) == [
        version for i in range(1, int(max(seen)) + 1)
        if (version := f'{i:04}') in seen or version not in RESERVED_MIGRATION_VERSIONS
    ]


def migration_files(directory):
    files = {}
    for path in sorted(directory.glob('*.up.sql')):
        require(re.fullmatch(r'[0-9]{4}_[A-Za-z0-9_]+\.up\.sql', path.name) is not None,
                'Invalid migration filename')
        version = path.name[:4]
        require(version not in files, 'Duplicate migration version')
        files[version] = path
    require(migration_sequence(files),
            'Migration source must be contiguous from 0001')
    return files


def migration_plan(files, applied):
    require(all(version in files for version in applied),
            'Database has migrations absent from the selected release; downgrade refused')
    require(migration_sequence(applied), 'Database migration history has gaps or invalid versions')
    require('0035' in applied, 'Schemas older than 0035 require a separately reviewed bootstrap')
    pending = [p for version, p in files.items() if version not in applied]
    for path in pending:
        reviewed_digest = REVIEWED_EXPAND_MIGRATIONS.get(path.name)
        if reviewed_digest is not None:
            require(hashlib.sha256(path.read_bytes()).hexdigest() == reviewed_digest,
                    'Reviewed migration content changed; preserve existing SQL and add a forward migration')
        require(reviewed_digest is not None or '-- deploy: expand' in path.read_text().splitlines()[:10],
                'Pending migrations must explicitly declare -- deploy: expand; use maintenance for contract changes')
    return pending


def hashes(files):
    return {v: hashlib.sha256(p.read_bytes()).hexdigest() for v, p in files.items()}


def status(root, sha):
    path = root / (sha + '.json')
    if not path.exists():
        return {'sha': sha, 'status': 'unknown'}
    state = json.loads(path.read_text())
    if state['status'] in {'queued', 'running'}:
        pid = root / (sha + '.pid')
        try:
            os.kill(int(pid.read_text()), 0)
        except (FileNotFoundError, ProcessLookupError, ValueError):
            # Do not overwrite a result published just before the worker exited.
            state = json.loads(path.read_text())
            if state['status'] in {'queued', 'running'}:
                state.update(status='failed', error='Release worker stopped; inspect the VPS before retrying')
                atomic_json(path, state)
    return state


def prune(root, now, apply=False):
    """Expire only recognized, terminal attempts; keep journal and hash ledger."""
    expired = []
    directory = root / 'releases'
    if not directory.exists():
        return expired
    require(not directory.is_symlink(), 'Release directory cannot be a symlink')
    for path in directory.iterdir():
        if path.is_symlink() or not path.is_dir() or not re.fullmatch(r'[0-9a-f]{40}-[0-9a-f]{12}', path.name):
            continue
        result = path / 'result.json'
        if not result.is_file() or result.is_symlink():
            continue
        value = json.loads(result.read_text())
        if value.get('status') not in {'success', 'failed', 'rehearsal_success'}:
            continue
        updated = datetime.datetime.fromisoformat(value['updated_at'])
        if updated < now - datetime.timedelta(days=60):
            expired.append(path.name)
            if apply:
                shutil.rmtree(path)
    return expired


def validate_history(applied, incoming, known):
    require(all(version in known and incoming.get(version) == known[version] for version in applied),
            'Applied migration content changed or has no trusted release record')


@contextlib.contextmanager
def lock(path):
    with path.open('a') as handle:
        try:
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ReleaseError('Another production release is active') from None
        yield


class Release:
    def __init__(self, sha, root=ROOT):
        require(SHA.fullmatch(sha) is not None, 'Invalid SHA')
        self.sha, self.root = sha, root
        self.directory = root / 'releases' / (sha + '-' + secrets.token_hex(6))
        self.directory.mkdir(parents=True, mode=0o700)
        self.source = self.directory / 'source'
        self.state = {'sha': sha, 'status': 'running', 'phase': 'starting', 'attempt': self.directory.name}
        self.pinned = False
        self.deployment = None
        self.log = (self.directory / 'private.log').open('a')

    def stage(self, phase, **values):
        self.state.update(phase=phase, updated_at=datetime.datetime.now(datetime.timezone.utc).isoformat(), **values)
        atomic_json(self.root / (self.sha + '.json'), self.state)
        atomic_json(self.directory / 'result.json', self.state)

    def run(self, args, *, input=None, binary=False, timeout=120):
        result = subprocess.run([str(a) for a in args], input=input, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, text=not binary, timeout=timeout)
        # All command output is private; exceptions must not include envs, SQL or URLs.
        for out in [result.stdout, result.stderr]:
            self.log.write(out.decode(errors='replace') if isinstance(out, bytes) else out)
        self.log.flush()
        require(result.returncode == 0, 'Release command failed; inspect the private release log on the VPS')
        return result.stdout

    def docker(self, *args, **kwargs):
        return self.run(['docker', *args], **kwargs)

    def inspect(self, name):
        return json.loads(self.docker('inspect', name))[0]

    @staticmethod
    def env(container):
        return dict(v.split('=', 1) for v in container['Config']['Env'] if '=' in v)

    def coolify(self, path, method='GET', payload=None):
        request = urllib.request.Request('http://localhost:8000/api/v1' + path,
            data=json.dumps(payload).encode() if payload is not None else None,
            headers={'Authorization': 'Bearer ' + TOKEN.read_text().strip(), 'Content-Type': 'application/json'},
            method=method)
        with urllib.request.urlopen(request, timeout=30) as response:
            return json.load(response)

    def sql(self, name, query):
        environment = self.env(self.inspect(name))
        output = self.docker('exec', name, 'psql', '-X', '-U', environment.get('POSTGRES_USER', 'postgres'),
            '-d', environment.get('POSTGRES_DB', environment.get('POSTGRES_USER', 'postgres')),
            '-v', 'ON_ERROR_STOP=1', '-A', '-t', '-c',
            "BEGIN READ ONLY; SET LOCAL statement_timeout='15s'; " + query + '; ROLLBACK;')
        return [json.loads(line) for line in output.splitlines() if line.startswith(('{', '['))]

    def applied(self, name):
        return self.sql(name, "SELECT coalesce(jsonb_agg(trim(version) ORDER BY version),'[]'::jsonb) FROM schema_migrations")[0]

    def source_current(self):
        self.run(['git', '-C', self.source, 'fetch', '--no-tags', 'origin', 'main'])
        head = self.run(['git', '-C', self.source, 'rev-parse', 'FETCH_HEAD']).strip()
        require(head == self.sha, 'Release superseded by current remote main; no older revision may be deployed')
        require(self.run(['git', '-C', self.source, 'rev-parse', 'HEAD']).strip() == head,
                'Checkout differs from fetched main')
        require(not self.run(['git', '-C', self.source, 'status', '--porcelain', '--untracked-files=all']).strip(),
                'Build source is dirty')

    def verify_ci(self):
        def github(path):
            request = urllib.request.Request('https://api.github.com/repos/am-p/app-loyalty' + path,
                headers={'Accept': 'application/vnd.github+json', 'User-Agent': 'puntazo-production-release'})
            with urllib.request.urlopen(request, timeout=30) as response:
                return json.load(response)
        runs = github('/actions/runs?head_sha=' + self.sha + '&per_page=30')['workflow_runs']
        for run in runs:
            if run['head_sha'] != self.sha or run['head_branch'] != 'main' or run['event'] not in {'push', 'workflow_dispatch'}:
                continue
            if run.get('path') != '.github/workflows/deploy-production.yml':
                continue
            jobs = github('/actions/runs/' + str(run['id']) + '/jobs?filter=latest')['jobs']
            if any(job['name'] == 'verify / verify' and job['conclusion'] == 'success' for job in jobs):
                return
        raise ReleaseError('Current main has no successful production verify job; deployment refused')

    def prepare(self):
        self.stage('source')
        self.run(['git', 'clone', '--single-branch', '--branch', 'main', REPOSITORY, self.source])
        self.source_current()
        self.verify_ci()
        controller = self.source / 'ops' / 'production_release.py'
        require(controller.is_file() and hashlib.sha256(controller.read_bytes()).digest()
                == hashlib.sha256(Path(__file__).read_bytes()).digest(),
                'Install the reviewed controller matching current main before releasing')
        self.files = migration_files(self.source / 'migrations')
        self.target = list(self.files)[-1]
        self.app = self.coolify('/applications/' + APP)
        require(self.app['git_repository'].removesuffix('.git') == 'am-p/app-loyalty'
                and self.app['git_branch'] == 'main' and self.app['git_commit_sha'] == 'HEAD',
                'Coolify must select the official main branch with HEAD')
        self.no_active_deployment()
        names = self.docker('ps', '--format', '{{.Names}}').splitlines()
        names = [n for n in names if n.startswith(APP + '-')]
        require(len(names) == 1, 'Expected exactly one production API; reconcile active rollouts first')
        self.previous = self.inspect(names[0])
        self.environment = self.env(self.previous)
        db = self.inspect(DB)
        from urllib.parse import urlsplit
        aliases = [v for n in db['NetworkSettings']['Networks'].values() for v in (n.get('Aliases') or [])]
        require(urlsplit(self.environment['DATABASE_URL']).hostname in [DB, *aliases], 'Production database target mismatch')
        self.network = next(iter(db['NetworkSettings']['Networks']))
        self.versions = self.applied(DB)
        self.pending = migration_plan(self.files, self.versions)
        self.digests = hashes(self.files)
        ledger = self.root / 'migration-hashes.json'
        if ledger.exists():
            self.known = json.loads(ledger.read_text())
        else:
            # Bootstrap from the actual running image, never from a historical report.
            self.known = {}
            for version in self.versions:
                path = self.files[version]
                raw = self.docker('exec', names[0], 'cat', '/migrations/' + path.name, binary=True)
                self.known[version] = hashlib.sha256(raw).hexdigest()
        validate_history(self.versions, self.digests, self.known)
        atomic_json(self.directory / 'source-migrations.json', self.digests)
        atomic_json(self.directory / 'before-app.json', self.app)
        configured = self.coolify('/applications/' + APP + '/envs')
        require(len([v for v in configured if v['key'] == 'EXPECTED_SCHEMA_VERSION' and not v.get('is_preview')]) <= 1,
                'Duplicate normal schema variables; manual reconciliation required')
        atomic_json(self.directory / 'before-envs.json', configured)
        self.stage('build', schema_before=self.versions[-1], schema_target=self.target, pending=len(self.pending))
        self.source_current()
        self.image = 'puntazo-production-candidate:' + self.sha
        self.run(['docker', 'build', '--target', 'api', '-t', self.image, self.source], timeout=900)

    def no_active_deployment(self):
        active = self.coolify('/deployments')
        require(isinstance(active, list), 'Cannot verify the Coolify deployment queue')
        require(not any(str(row.get('application_id')) == str(self.app['id'])
                        and row.get('status') in {'queued', 'in_progress'} for row in active),
                'A Coolify production deployment is already active; wait before releasing')

    def environment_file(self, name, values):
        require(all('\n' not in str(v) and '\r' not in str(v) for v in values.values()), 'Invalid multiline environment')
        path = self.directory / name
        path.write_text(''.join(k + '=' + str(v) + '\n' for k, v in values.items()))
        path.chmod(0o600)
        return path

    def image_command(self, executable, values, arguments, network=None, mounts=()):
        environment = self.environment_file('job.env', values)
        args = ['run', '--rm', '--memory', '256m', '--cpus', '0.5', '--network', network or self.network,
                '--env-file', environment]
        for source, destination in mounts:
            args += ['-v', str(source) + ':' + destination]
        return self.docker(*args, '--entrypoint', executable, self.image, *arguments, timeout=180)

    def backup_and_rehearse(self):
        try:
            return self._backup_and_rehearse()
        finally:
            (self.directory / 'production.dump').unlink(missing_ok=True)

    def _backup_and_rehearse(self):
        if not self.pending:
            return
        self.stage('backup')
        journal = self.root / 'deletions'
        journal.mkdir(mode=0o700, exist_ok=True)
        # Export a cumulative, external journal before taking any new dump.
        env = self.environment_file('journal.env', {'DATABASE_URL': self.environment['DATABASE_URL']})
        self.docker('run', '--rm', '--user', '0', '--network', self.network, '--env-file', env,
            '-v', str(journal) + ':/journal', '--entrypoint', '/usr/local/bin/puntazo-deletion-journal', self.image,
            '-mode', 'export', '-environment', 'production', '-file', '/journal/production.json')
        dump = self.directory / 'production.dump'
        de = self.env(self.inspect(DB))
        with dump.open('wb') as handle:
            subprocess.run(['docker', 'exec', DB, 'pg_dump', '-U', de.get('POSTGRES_USER', 'postgres'),
                '-d', de.get('POSTGRES_DB', de.get('POSTGRES_USER', 'postgres')), '-Fc', '--no-owner', '--no-privileges'],
                stdout=handle, stderr=self.log, check=True, timeout=120)
        original = hashlib.sha256(dump.read_bytes()).hexdigest()
        key = self.directory / 'backup.key'
        key.write_text(secrets.token_hex(32))
        encrypted = self.directory / 'production.dump.enc'
        self.run(['openssl', 'enc', '-aes-256-cbc', '-pbkdf2', '-salt', '-in', dump, '-out', encrypted, '-pass', 'file:' + str(key)])
        dump.unlink()
        self.run(['openssl', 'enc', '-d', '-aes-256-cbc', '-pbkdf2', '-in', encrypted, '-out', dump, '-pass', 'file:' + str(key)])
        require(hashlib.sha256(dump.read_bytes()).hexdigest() == original, 'Backup decrypt checksum failed')
        atomic_json(self.directory / 'backup.json', {'encrypted_sha256': hashlib.sha256(encrypted.read_bytes()).hexdigest(),
                                                   'dump_sha256': original})
        self.stage('rehearsal')
        name = 'puntazo-release-rehearsal-' + secrets.token_hex(6)
        password = secrets.token_urlsafe(32)
        env = self.environment_file('rehearsal.env', {'POSTGRES_USER': 'rehearsal', 'POSTGRES_DB': 'rehearsal', 'POSTGRES_PASSWORD': password})
        try:
            self.docker('run', '-d', '--name', name, '--network', 'none', '--memory', '768m', '--cpus', '1',
                '--tmpfs', '/var/lib/postgresql/data:rw,size=1g', '--env-file', env, 'postgres:16-alpine')
            for attempt in range(45):
                ready = subprocess.run(['docker', 'exec', name, 'pg_isready', '-h', '127.0.0.1', '-U', 'rehearsal', '-d', 'rehearsal'],
                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0
                if ready:
                    break
                time.sleep(1)
            require(ready, 'Isolated restore database did not become ready')
            with dump.open('rb') as handle:
                subprocess.run(['docker', 'exec', '-i', name, 'pg_restore', '--exit-on-error', '--no-owner', '--no-privileges',
                    '-U', 'rehearsal', '-d', 'rehearsal'], stdin=handle, stdout=self.log, stderr=self.log, check=True, timeout=120)
            url = 'postgres://rehearsal:' + password + '@127.0.0.1:5432/rehearsal?sslmode=disable'
            values = {'DATABASE_URL': url, 'MIGRATIONS_DIR': '/migrations'}
            self.image_command('puntazo-migrate', values, ['up'], network='container:' + name)
            require(not self.image_command('puntazo-migrate', values, ['up'], network='container:' + name).strip(),
                    'Second migration pass was not idempotent')
            require(self.applied(name) == list(self.files), 'Restored database did not reach the release schema')
            self.probe_candidate(url, name)
            self.stage('rehearsed', backup_verified=True)
        finally:
            dump.unlink(missing_ok=True)
            subprocess.run(['docker', 'rm', '-f', name], stdout=self.log, stderr=self.log, check=False)

    def probe_candidate(self, url, database):
        values = dict(self.environment)
        values.update(DATABASE_URL=url, EXPECTED_SCHEMA_VERSION=self.target, GIT_COMMIT=self.sha,
            APP_ENV='development', MAIL_PROVIDER='disabled', MEDIA_PROVIDER='disabled', RATE_LIMIT_PROVIDER='memory',
            EMAIL_VERIFICATION_REQUIRED='false', LOG_DIRECTORY='',
            CAPTCHA_ENABLED='false', MERCADO_PAGO_PROVIDER='disabled', MERCADO_PAGO_ACCESS_TOKEN='',
            MERCADO_PAGO_WEBHOOK_SECRET='', BRANCH_PRORATION_ENABLED='false', BRANCH_PAYMENT_SIMULATOR='false', PORT='8080')
        environment = self.environment_file('candidate.env', values)
        name = database + '-api'
        try:
            self.docker('run', '-d', '--name', name, '--network', 'container:' + database,
                '--memory', '768m', '--cpus', '1', '--env-file', environment, self.image)
            for attempt in range(30):
                result = subprocess.run(['docker', 'exec', name, 'wget', '-qO-', 'http://127.0.0.1:8080/v1/health/ready'],
                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                if result.returncode == 0:
                    return
                time.sleep(1)
            raise ReleaseError('Candidate API did not become ready on the migrated isolated copy')
        finally:
            subprocess.run(['docker', 'logs', name], stdout=self.log, stderr=self.log, check=False)
            subprocess.run(['docker', 'rm', '-f', name], stdout=self.log, stderr=self.log, check=False)

    def update_schema(self):
        values = self.coolify('/applications/' + APP + '/envs')
        normal = [v for v in values if v['key'] == 'EXPECTED_SCHEMA_VERSION' and not v.get('is_preview')]
        require(len(normal) <= 1, 'Duplicate normal schema variables; manual reconciliation required')
        self.coolify('/applications/' + APP + '/envs/bulk', 'PATCH', {'data': [
            {'key': 'EXPECTED_SCHEMA_VERSION', 'value': self.target, 'is_preview': False, 'is_buildtime': False, 'is_runtime': True}]})
        after = self.coolify('/applications/' + APP + '/envs')
        require(any(v['key'] == 'EXPECTED_SCHEMA_VERSION' and not v.get('is_preview') and v['value'] == self.target for v in after),
                'Runtime schema configuration was not updated')

    def promote(self):
        # Re-fetch immediately before live mutation. Freeze only this deployment,
        # restoring HEAD after Coolify reaches a terminal state.
        self.source_current()
        self.no_active_deployment()
        require(self.applied(DB) == self.versions, 'Database changed after the release rehearsal; restart verification')
        self.stage('migrate')
        try:
            self.image_command('puntazo-migrate', {'DATABASE_URL': self.environment['DATABASE_URL'], 'MIGRATIONS_DIR': '/migrations'}, ['up'])
        finally:
            applied = self.applied(DB)
            require(set(applied) <= set(self.files), 'Unknown migration appeared during release')
            atomic_json(self.root / 'migration-hashes.json', {v: self.digests[v] for v in applied})
        require(applied == list(self.files), 'Production did not reach the target schema')
        self.update_schema()
        terminal = False
        try:
            self.coolify('/applications/' + APP, 'PATCH', {'git_commit_sha': self.sha})
            self.pinned = True
            self.stage('deploy')
            response = self.coolify('/deploy?uuid=' + APP + '&force=false', 'POST')
            self.deployment = response['deployments'][0]['deployment_uuid']
            self.stage('deploy', deployment_uuid=self.deployment)
            for attempt in range(150):
                details = self.coolify('/deployments/' + self.deployment)
                if details['status'] in TERMINAL:
                    terminal = True
                    require(details['status'] == 'finished' and details['commit'] == self.sha,
                            'Coolify failed or deployed a different revision; inspect the VPS release record')
                    break
                time.sleep(10)
            require(terminal, 'Coolify is still active; leave revision frozen and reconcile before another release')
        finally:
            if terminal:
                app = self.coolify('/applications/' + APP)
                require(app['git_commit_sha'] == self.sha, 'Release revision changed externally; reconcile configuration')
                self.coolify('/applications/' + APP, 'PATCH', {'git_commit_sha': 'HEAD'})
                self.pinned = False
        self.stage('verify')
        consecutive = 0
        for attempt in range(30):
            try:
                with urllib.request.urlopen(PUBLIC + '/v1/version', timeout=10) as response:
                    version = json.load(response)
                with urllib.request.urlopen(PUBLIC + '/v1/health/ready', timeout=10) as response:
                    ready = response.status == 200
                if version.get('commit') == self.sha and version.get('schema_version') == self.target and ready:
                    consecutive += 1
                    if consecutive >= 3:
                        self.stage('done', status='success')
                        return
                else:
                    consecutive = 0
            except (OSError, ValueError):
                consecutive = 0
            time.sleep(5)
        raise ReleaseError('Public commit, schema or readiness verification failed')

    def execute(self, rehearse_only=False):
        try:
            with lock(self.root / 'release.lock'):
                self.prepare()
                self.backup_and_rehearse()
                if rehearse_only:
                    self.stage('done', status='rehearsal_success')
                else:
                    self.promote()
        except Exception as error:
            traceback.print_exc(file=self.log)
            message = str(error) if isinstance(error, ReleaseError) else 'Release failed; inspect the private VPS log'
            self.stage('failed', status='failed', error=message, revision_frozen=self.pinned)
            # Never down-migrate or restore the database on failure.
        finally:
            self.log.close()
        return self.state


def main():
    if len(sys.argv) == 3 and sys.argv[1] == '--check-migrations' and not os.environ.get('SSH_ORIGINAL_COMMAND'):
        files = migration_files(Path(sys.argv[2]))
        migration_plan(files, list(files)[:35])
        print(json.dumps({'migration_policy': 'valid', 'latest_schema': list(files)[-1]}))
        return
    if len(sys.argv) == 2 and sys.argv[1] in {'--prune', '--prune-apply'} and not os.environ.get('SSH_ORIGINAL_COMMAND'):
        apply = sys.argv[1] == '--prune-apply'
        print(json.dumps({'apply': apply, 'expired_attempts': prune(ROOT, datetime.datetime.now(datetime.timezone.utc), apply)}))
        return
    os.umask(0o077)
    ROOT.mkdir(mode=0o700, exist_ok=True)
    if len(sys.argv) == 3 and sys.argv[1] == '--worker':
        Release(sys.argv[2]).execute()
        return
    if len(sys.argv) == 3 and sys.argv[1] == '--rehearse' and not os.environ.get('SSH_ORIGINAL_COMMAND'):
        result = Release(sys.argv[2]).execute(rehearse_only=True)
        print(json.dumps(result))
        sys.exit(0 if result['status'] == 'rehearsal_success' else 1)
    operation, sha = command(os.environ.get('SSH_ORIGINAL_COMMAND', ''))
    path = ROOT / (sha + '.json')
    if operation == 'status':
        with lock(ROOT / 'dispatch.lock'):
            print(json.dumps(status(ROOT, sha)))
        return
    with lock(ROOT / 'dispatch.lock'):
        if path.exists():
            state = status(ROOT, sha)
            if state['status'] in {'queued', 'running'}:
                print(json.dumps(state))
                return
        atomic_json(path, {'sha': sha, 'status': 'queued', 'phase': 'queued'})
        with (ROOT / 'dispatch.log').open('a') as output:
            child = subprocess.Popen([sys.executable, str(Path(__file__).resolve()), '--worker', sha],
                stdin=subprocess.DEVNULL, stdout=output, stderr=output, start_new_session=True, close_fds=True)
            (ROOT / (sha + '.pid')).write_text(str(child.pid))
        print(json.dumps({'sha': sha, 'status': 'queued'}))


if __name__ == '__main__':
    try:
        main()
    except ReleaseError as error:
        print(json.dumps({'status': 'failed', 'error': str(error)}))
        sys.exit(1)
