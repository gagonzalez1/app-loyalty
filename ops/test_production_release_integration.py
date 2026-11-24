#!/usr/bin/env python3
"""Real Docker/PostgreSQL test. No production database, credentials or ports."""
import argparse
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import tempfile

import production_release as release


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True, help='Already built API image, e.g. puntazo-api:ci')
    args = parser.parse_args()
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix='puntazo-release-test-') as directory:
        root = Path(directory)
        database = 'puntazo-release-test-' + secrets.token_hex(6)
        release.DB = database  # All database commands are redirected to this disposable fixture.
        password = secrets.token_urlsafe(24)
        url = 'postgres://fixture:' + password + '@127.0.0.1:5432/fixture?sslmode=disable'
        db_env = root / 'db.env'
        db_env.write_text('POSTGRES_USER=fixture\nPOSTGRES_DB=fixture\nPOSTGRES_PASSWORD=' + password + '\n')
        fixture = root / 'migrations'
        shutil.copytree(Path(__file__).resolve().parents[1] / 'migrations', fixture)
        source_files = release.migration_files(fixture)
        versions = list(source_files)
        assert len(versions) >= 2, 'Release rehearsal needs at least two source migrations'
        previous, latest = versions[-2:]
        # Rehearse the real source-tip migration so the candidate binary's embedded
        # schema manifest can validate readiness after upgrading the restored copy.
        latest_path = source_files[latest]
        held_latest = root / latest_path.name
        failure_version = f'{int(latest) + 1:04}'

        class FixtureRelease(release.Release):
            def __init__(self, sha):
                super().__init__(sha, root)
                self.image = args.image
                self.network = 'container:' + database
                self.environment = {'DATABASE_URL': url, 'JWT_SECRET': 'x' * 32,
                                    'QR_PEPPER': 'y' * 32, 'PUBLIC_APP_URL': 'http://localhost:8080'}

            def image_command(self, executable, values, arguments, network=None, mounts=()):
                # The synthetic SQL is bind-mounted, so image-layer permissions
                # do not apply to this isolated fixture.
                fixture.chmod(0o755)
                for path in fixture.glob('*.sql'):
                    path.chmod(0o644)
                return super().image_command(executable, values, arguments, network,
                                             mounts=(*mounts, (fixture, '/migrations:ro')))

        worker = FixtureRelease('a' * 40)
        try:
            worker.docker('run', '-d', '--name', database, '--network', 'none', '--memory', '768m', '--cpus', '1',
                '--tmpfs', '/var/lib/postgresql/data:rw,size=1g', '--env-file', db_env, 'postgres:16-alpine')
            ready = False
            for _ in range(45):
                if subprocess.run(['docker', 'exec', database, 'pg_isready', '-h', '127.0.0.1', '-U', 'fixture', '-d', 'fixture'],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
                    ready = True
                    break
                release.time.sleep(1)
            release.require(ready, 'Fixture PostgreSQL not ready')
            latest_path.replace(held_latest)
            try:
                worker.image_command('puntazo-migrate', {'DATABASE_URL': url, 'MIGRATIONS_DIR': '/migrations'}, ['up'])
            finally:
                held_latest.replace(latest_path)
            worker.docker('exec', database, 'psql', '-U', 'fixture', '-d', 'fixture', '-v', 'ON_ERROR_STOP=1', '-c',
                "CREATE TABLE release_preserved(id INT PRIMARY KEY, value TEXT); INSERT INTO release_preserved VALUES(1,'preserved');")
            worker.files = release.migration_files(fixture)
            worker.target = list(worker.files)[-1]
            worker.pending = release.migration_plan(worker.files, worker.applied(database))
            worker.backup_and_rehearse()
            assert worker.applied(database)[-1] == previous, 'Rehearsal modified the live fixture'
            assert not (worker.directory / 'production.dump').exists(), 'Plaintext dump left behind'
            assert (worker.directory / 'production.dump.enc').exists()
            worker.image_command('puntazo-migrate', {'DATABASE_URL': url, 'MIGRATIONS_DIR': '/migrations'}, ['up'])
            assert worker.applied(database)[-1] == latest
            preserved = worker.sql(database, 'SELECT json_build_object(\'value\',value) FROM release_preserved WHERE id=1')[0]
            assert preserved['value'] == 'preserved'
            worker.log.close()
            worker = FixtureRelease('b' * 40)
            (fixture / (failure_version + '_failure_fixture.up.sql')).write_text(
                "-- deploy: expand\nDO $$ BEGIN RAISE EXCEPTION 'intentional rehearsal failure'; END $$;\n")
            (fixture / (failure_version + '_failure_fixture.up.sql')).chmod(0o644)
            worker.files = release.migration_files(fixture)
            worker.target = list(worker.files)[-1]
            worker.pending = release.migration_plan(worker.files, worker.applied(database))
            failed = False
            try:
                worker.backup_and_rehearse()
            except release.ReleaseError:
                failed = True
            assert failed, 'Invalid migration was accepted'
            assert worker.applied(database)[-1] == latest, 'Failed rehearsal changed live fixture'
            assert not (worker.directory / 'production.dump').exists(), 'Failed rehearsal left plaintext'
            print(json.dumps({'isolated': True, 'restored_and_upgraded': latest, 'idempotent': True,
                'candidate_ready': True, 'existing_data_preserved': True, 'failed_migration_blocked': failure_version,
                'production_touched': False}))
        except Exception:
            # This fixture starts empty, uses no production credentials or data,
            # and redirects every DB command to the disposable container above.
            worker.log.flush()
            diagnostic = (worker.directory / 'private.log').read_text()[-4000:]
            print('Isolated fixture diagnostics:\n' + diagnostic.replace(password, '<fixture-password>'))
            raise
        finally:
            subprocess.run(['docker', 'rm', '-f', database], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            worker.log.close()


if __name__ == '__main__':
    main()
