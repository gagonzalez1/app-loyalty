import contextlib
import datetime
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

import production_release as release


SHA = 'a' * 40


class SafetyTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)

    def files(self, count=35):
        for i in range(1, count + 1):
            (self.root / f'{i:04}_migration.up.sql').write_text('-- deploy: expand\nSELECT 1;\n')
        return release.migration_files(self.root)

    def worker(self):
        worker = release.Release(SHA, self.root)
        self.addCleanup(worker.log.close)
        return worker

    def test_only_two_exact_ssh_commands_are_allowed(self):
        for operation in ['deploy', 'status']:
            self.assertEqual(release.command(operation + ' ' + SHA), (operation, SHA))
        for text in ['bash', 'deploy main', 'deploy ' + SHA + ';id', 'status ' + SHA + '\n',
                     'deploy ' + SHA + ' extra', 'rehearse ' + SHA, 'status ../../etc/shadow',
                     'deploy ' + SHA.upper(), 'scp -t /tmp/file', 'deploy $(id)']:
            with self.subTest(text=text), self.assertRaises(release.ReleaseError):
                release.command(text)

    def test_no_change_release_needs_no_migration(self):
        files = self.files()
        self.assertEqual(release.migration_plan(files, list(files)), [])

    def test_future_expand_migration_is_accepted_without_config_version_edit(self):
        files = self.files(36)
        self.assertEqual([p.name for p in release.migration_plan(files, list(files)[:35])], ['0036_migration.up.sql'])

    def test_undeclared_contract_change_stops_before_production(self):
        files = self.files(36)
        files['0036'].write_text('DROP TABLE usuarios;\n')
        with self.assertRaisesRegex(release.ReleaseError, 'explicitly declare'):
            release.migration_plan(files, list(files)[:35])

    def reviewed_files(self):
        files = self.files()
        directory = Path(__file__).resolve().parents[1] / 'migrations'
        for name in release.REVIEWED_EXPAND_MIGRATIONS:
            source = directory / name
            if source.exists():
                destination = self.root / name
                destination.write_bytes(source.read_bytes())
                files[name[:4]] = destination
        return files

    def test_existing_reviewed_expansions_preserve_sql_bytes(self):
        files = self.reviewed_files()
        self.assertEqual(release.migration_plan(files, list(files)[:35]), list(files.values())[35:])

    def test_changed_reviewed_expansion_is_rejected_even_with_marker(self):
        files = self.reviewed_files()
        files['0036'].write_text('-- deploy: expand\nDROP TABLE usuarios;\n')
        with self.assertRaisesRegex(release.ReleaseError, 'Reviewed migration content changed'):
            release.migration_plan(files, list(files)[:35])

    def test_renamed_reviewed_expansion_is_not_implicitly_accepted(self):
        files = self.reviewed_files()
        renamed = self.root / '0036_other.up.sql'
        files['0036'].rename(renamed)
        files['0036'] = renamed
        with self.assertRaisesRegex(release.ReleaseError, 'explicitly declare'):
            release.migration_plan(files, list(files)[:35])

    def test_history_gaps_ahead_and_old_bootstrap_are_rejected(self):
        files = self.files()
        for applied in [list(files)[:-1], list(files) + ['0036'], list(files)[:20] + list(files)[21:]]:
            with self.subTest(applied=applied), self.assertRaises(release.ReleaseError):
                release.migration_plan(files, applied)

    def test_source_numbering_collision_and_gaps_are_rejected(self):
        self.files()
        (self.root / '0035_duplicate.up.sql').write_text('SELECT 1;')
        with self.assertRaisesRegex(release.ReleaseError, 'Duplicate'):
            release.migration_files(self.root)
        (self.root / '0035_duplicate.up.sql').unlink()
        (self.root / '0010_migration.up.sql').unlink()
        with self.assertRaisesRegex(release.ReleaseError, 'contiguous'):
            release.migration_files(self.root)

    def test_reserved_apple_versions_do_not_block_independent_migration(self):
        files = self.files(46)
        applied = list(files)[:44]
        files['0045'].unlink()
        without_billing = release.migration_files(self.root)
        self.assertEqual(release.migration_plan(without_billing, applied), [files['0046']])
        self.assertEqual(release.migration_plan(without_billing, list(without_billing)), [])
        files['0045'].write_text('-- deploy: expand\nSELECT 1;\n')
        self.assertEqual(release.migration_plan(release.migration_files(self.root), list(without_billing)),
                         [files['0045']])
        files['0044'].unlink()
        with self.assertRaisesRegex(release.ReleaseError, 'downgrade refused'):
            release.migration_plan(release.migration_files(self.root), applied)

    def test_applied_sql_cannot_be_rewritten_or_renamed(self):
        incoming = {'0035': 'digest'}
        release.validate_history(['0035'], incoming, incoming)
        for known in [{}, {'0035': 'different'}]:
            with self.assertRaises(release.ReleaseError):
                release.validate_history(['0035'], incoming, known)

    def test_backup_failure_never_reaches_live_migration(self):
        worker = self.worker()
        worker.prepare = Mock()
        worker.backup_and_rehearse = Mock(side_effect=release.ReleaseError('Backup decrypt checksum failed'))
        worker.promote = Mock()
        self.assertEqual(worker.execute()['status'], 'failed')
        worker.promote.assert_not_called()

    def test_rehearsal_failure_never_reaches_live_migration(self):
        worker = self.worker()
        worker.prepare = Mock()
        worker.backup_and_rehearse = Mock(side_effect=release.ReleaseError('Candidate API did not become ready'))
        worker.promote = Mock()
        worker.execute()
        worker.promote.assert_not_called()

    def test_non_protocol_exceptions_do_not_leak_credentials(self):
        worker = self.worker()
        worker.prepare = Mock(side_effect=RuntimeError('postgres://user:secret@example/db'))
        state = worker.execute()
        self.assertNotIn('secret', json.dumps(state))
        self.assertIn('secret', (worker.directory / 'private.log').read_text())

    def test_release_lock_refuses_another_migration(self):
        with release.lock(self.root / 'release.lock'):
            with self.assertRaises(release.ReleaseError):
                with release.lock(self.root / 'release.lock'):
                    self.fail('Second migration acquired the lock')

    def test_stale_sha_or_dirty_source_is_rejected_before_build(self):
        worker = self.worker()
        worker.run = Mock(side_effect=['', 'b' * 40])
        with self.assertRaisesRegex(release.ReleaseError, 'superseded'):
            worker.source_current()
        worker.run = Mock(side_effect=['', SHA, SHA, ' M migrations/0036.sql'])
        with self.assertRaisesRegex(release.ReleaseError, 'dirty'):
            worker.source_current()

    def test_main_must_have_successful_verify_job(self):
        worker = self.worker()
        def response(value):
            return contextlib.closing(io.BytesIO(json.dumps(value).encode()))
        run = {'id': 1, 'head_sha': SHA, 'head_branch': 'main', 'event': 'push',
               'path': '.github/workflows/deploy-production.yml'}
        with patch.object(release.urllib.request, 'urlopen', side_effect=[response({'workflow_runs': [run]}),
                         response({'jobs': [{'name': 'verify / verify', 'conclusion': 'failure'}]})]):
            with self.assertRaisesRegex(release.ReleaseError, 'no successful'):
                worker.verify_ci()
        with patch.object(release.urllib.request, 'urlopen', side_effect=[response({'workflow_runs': [run]}),
                         response({'jobs': [{'name': 'verify / verify', 'conclusion': 'success'}]})]):
            worker.verify_ci()

    def promotion_worker(self):
        worker = self.worker()
        worker.files = {'0035': Path('35'), '0036': Path('36')}
        worker.digests = {'0035': 'old', '0036': 'new'}
        worker.environment = {'DATABASE_URL': 'sensitive'}
        worker.target = '0036'
        worker.source_current = Mock()
        worker.no_active_deployment = Mock()
        worker.versions = ['0035']
        worker.image_command = Mock()
        worker.applied = Mock(side_effect=lambda db: list(worker.files) if worker.image_command.call_count else worker.versions)
        worker.update_schema = Mock()
        return worker

    def test_partial_migration_is_recorded_without_down_or_deployment(self):
        worker = self.promotion_worker()
        worker.image_command.side_effect = release.ReleaseError('Migration failed')
        worker.applied = Mock(return_value=['0035'])
        worker.coolify = Mock()
        with self.assertRaisesRegex(release.ReleaseError, 'Migration failed'):
            worker.promote()
        worker.update_schema.assert_not_called()
        worker.coolify.assert_not_called()
        self.assertEqual(json.loads((self.root / 'migration-hashes.json').read_text()), {'0035': 'old'})
        self.assertEqual(worker.image_command.call_args.args[-1], ['up'])

    def test_release_superseded_after_rehearsal_never_migrates(self):
        worker = self.promotion_worker()
        worker.source_current.side_effect = release.ReleaseError('superseded')
        with self.assertRaises(release.ReleaseError):
            worker.promote()
        worker.image_command.assert_not_called()

    def test_failed_rollout_restores_head_without_database_downgrade(self):
        worker = self.promotion_worker()
        calls = []
        def api(path, method='GET', payload=None):
            calls.append((path, method, payload))
            if path.startswith('/deploy?'):
                return {'deployments': [{'deployment_uuid': 'deployment'}]}
            if path == '/deployments/deployment':
                return {'status': 'failed', 'commit': SHA}
            if method == 'GET':
                return {'git_commit_sha': SHA}
            return {}
        worker.coolify = api
        with self.assertRaisesRegex(release.ReleaseError, 'Coolify failed'):
            worker.promote()
        self.assertEqual(calls[-1][2], {'git_commit_sha': 'HEAD'})
        self.assertFalse(worker.pinned)
        self.assertEqual(worker.image_command.call_count, 1)

    def test_success_requires_expected_public_commit_schema_and_readiness(self):
        worker = self.promotion_worker()
        def api(path, method='GET', payload=None):
            if path.startswith('/deploy?'):
                return {'deployments': [{'deployment_uuid': 'deployment'}]}
            if path == '/deployments/deployment':
                return {'status': 'finished', 'commit': SHA}
            return {'git_commit_sha': SHA}
        worker.coolify = api
        def public(url, timeout=10):
            response = io.BytesIO(json.dumps({'commit': SHA, 'schema_version': '0036'}).encode())
            response.status = 200
            return contextlib.closing(response)
        with patch.object(release.urllib.request, 'urlopen', side_effect=public), patch.object(release.time, 'sleep'):
            worker.promote()
        self.assertEqual(worker.state['status'], 'success')
        self.assertFalse(worker.pinned)
        worker.update_schema.assert_called_once()

    def test_unresolved_deploy_request_remains_frozen_for_reconciliation(self):
        worker = self.promotion_worker()
        def api(path, method='GET', payload=None):
            if path.startswith('/deploy?'):
                raise TimeoutError('request may have been accepted')
            return {}
        worker.coolify = Mock(side_effect=api)
        with self.assertRaises(TimeoutError):
            worker.promote()
        self.assertTrue(worker.pinned)
        self.assertNotIn({'git_commit_sha': 'HEAD'}, [c.args[2] for c in worker.coolify.call_args_list if len(c.args) > 2])

    def test_dead_worker_can_be_retried(self):
        release.atomic_json(self.root / (SHA + '.json'), {'sha': SHA, 'status': 'running'})
        state = release.status(self.root, SHA)
        self.assertEqual(state['status'], 'failed')

    def test_manual_coolify_deployment_blocks_another_release(self):
        worker = self.worker()
        worker.app = {'id': 12}
        worker.coolify = Mock(return_value=[{'application_id': 12, 'status': 'in_progress'}])
        with self.assertRaisesRegex(release.ReleaseError, 'already active'):
            worker.no_active_deployment()
        worker.coolify.return_value = [{'application_id': 99, 'status': 'in_progress'}]
        worker.no_active_deployment()

    def test_rehearsal_cleans_plaintext_even_when_encryption_fails(self):
        worker = self.worker()
        dump = worker.directory / 'production.dump'
        def fail():
            dump.write_text('sensitive fixture')
            raise release.ReleaseError('Encryption failed')
        worker._backup_and_rehearse = fail
        with self.assertRaises(release.ReleaseError):
            worker.backup_and_rehearse()
        self.assertFalse(dump.exists())

    def test_changed_database_after_rehearsal_stops_before_migration(self):
        worker = self.promotion_worker()
        worker.applied = Mock(return_value=['0035', '0036'])
        with self.assertRaisesRegex(release.ReleaseError, 'changed after'):
            worker.promote()
        worker.image_command.assert_not_called()

    def test_successful_worker_status_is_not_replaced(self):
        expected = {'sha': SHA, 'status': 'success', 'phase': 'done'}
        release.atomic_json(self.root / (SHA + '.json'), expected)
        self.assertEqual(release.status(self.root, SHA), expected)

    def test_retention_removes_only_expired_terminal_attempts(self):
        now = datetime.datetime.now(datetime.timezone.utc)
        expired = self.root / 'releases' / (SHA + '-' + '1' * 12)
        active = self.root / 'releases' / (SHA + '-' + '2' * 12)
        for path, status in [(expired, 'success'), (active, 'running')]:
            path.mkdir(parents=True)
            release.atomic_json(path / 'result.json', {'status': status, 'updated_at': (now - datetime.timedelta(days=61)).isoformat()})
        (self.root / 'deletions').mkdir()
        (self.root / 'deletions' / 'production.json').write_text('preserve journal')
        self.assertEqual(release.prune(self.root, now), [expired.name])
        self.assertTrue(expired.exists())
        release.prune(self.root, now, apply=True)
        self.assertFalse(expired.exists())
        self.assertTrue(active.exists())
        self.assertTrue((self.root / 'deletions' / 'production.json').exists())

    def test_retention_never_follows_an_attempt_symlink(self):
        (self.root / 'releases').mkdir()
        outside = self.root / 'preserve'
        outside.mkdir()
        (self.root / 'releases' / (SHA + '-' + '3' * 12)).symlink_to(outside, target_is_directory=True)
        self.assertEqual(release.prune(self.root, datetime.datetime.now(datetime.timezone.utc), apply=True), [])
        self.assertTrue(outside.exists())

    def test_ssh_dispatch_launches_detached_worker_and_deduplicates(self):
        with patch.object(release, 'ROOT', self.root), patch.dict(os.environ, {'SSH_ORIGINAL_COMMAND': 'deploy ' + SHA}), \
             patch.object(release.sys, 'argv', ['controller']), patch.object(release.subprocess, 'Popen') as popen, \
             contextlib.redirect_stdout(io.StringIO()):
            popen.return_value.pid = os.getpid()
            release.main()
            self.assertTrue(popen.call_args.kwargs['start_new_session'])
            self.assertTrue(popen.call_args.kwargs['close_fds'])
            release.main()
            self.assertEqual(popen.call_count, 1)


if __name__ == '__main__':
    unittest.main()
