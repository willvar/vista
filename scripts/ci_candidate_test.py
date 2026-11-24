import unittest
from unittest.mock import patch

from ci_release import CANDIDATE_STEPS, TARGETS, find_candidate, github_entries


class CandidateTests(unittest.TestCase):
    repo = "owner/demo"
    version = "v1.2.3"
    source = "a" * 40

    def run_info(self, run_id, **override):
        return {"id": run_id, "event": "workflow_dispatch", "head_sha": self.source,
                "head_repository": {"full_name": self.repo}, "display_title": f"Release candidate {self.version}",
                "run_attempt": 1, "status": "completed", "conclusion": "success",
                "updated_at": "2026-11-23T12:00:00Z", **override}

    def jobs(self, *, omit=None, failure=None, skip=None):
        results = []
        for name, required in CANDIDATE_STEPS.items():
            if name == omit:
                continue
            steps = [{"name": step, "conclusion": "skipped" if skip == step else "success"} for step in required]
            if failure and name == failure[0]:
                steps = [step for step in steps if step["name"] != failure[1]]
                steps.append({"name": failure[1], "conclusion": "failure"})
            results.append({"name": name, "status": "completed", "steps": steps,
                            "conclusion": "failure" if failure and name == failure[0] else "success"})
        return results

    def find(self, runs, *, attempts=None, artifacts=None, tagged_at=None):
        responses = {"workflow_runs": runs,
                     "artifacts": [{"name": "native-" + target.replace("/", "-"), "expired": False}
                                   for target in TARGETS] if artifacts is None else artifacts}

        def gh(endpoint, field, **query):
            self.assertTrue(endpoint.startswith(f"repos/{self.repo}/actions/"))
            if field == "jobs":
                _, run, _, attempt, _ = endpoint.rsplit("/", 4)
                return (attempts or {}).get((int(run), int(attempt)), self.jobs())
            if field == "workflow_runs":
                self.assertTrue(endpoint.endswith("/workflows/validate.yml/runs"))
                self.assertEqual(query, {"event": "workflow_dispatch", "head_sha": self.source})
            return responses[field]

        with patch("ci_release.github_entries", side_effect=gh):
            return find_candidate(self.repo, self.version, self.source, tagged_at)

    def test_complete_first_attempt_for_exact_version_and_commit(self):
        # A later result on another source is not the candidate for this tag.
        self.assertEqual(self.find([self.run_info(102, head_sha="b" * 40), self.run_info(101)]), 101)

    def test_wrong_candidate_identity_is_not_evidence(self):
        for overrides in (
            {"head_sha": "b" * 40}, {"display_title": "Release candidate v1.2.4"},
            {"display_title": "Validate main"}, {"event": "push"},
            {"head_repository": {"full_name": "other/demo"}},
        ):
            with self.subTest(overrides=overrides), self.assertRaisesRegex(ValueError, "no complete first-attempt candidate"):
                self.find([self.run_info(101, **overrides)])

    def test_candidate_must_finish_before_the_tag_date(self):
        with self.assertRaisesRegex(ValueError, "before signing the tag"):
            self.find([self.run_info(101)], tagged_at=0)

    def test_refuses_incomplete_or_retried_candidates(self):
        for run, jobs in (
            (self.run_info(101), self.jobs(omit="Linux browser mobile-landscape")),
            (self.run_info(101), self.jobs(omit="Verify candidate archives")),
            (self.run_info(101), self.jobs(skip="Run viewport browser checks")),
            (self.run_info(101, run_attempt=2), self.jobs()),
        ):
            with self.subTest(run=run, jobs=len(jobs)), self.assertRaisesRegex(ValueError, "no complete first-attempt candidate"):
                self.find([run], attempts={(101, 1): jobs})
        with self.assertRaisesRegex(ValueError, "is not complete"):
            self.find([self.run_info(101, status="in_progress", conclusion=None)])

    def test_diagnostic_retry_and_new_run_cannot_clear_a_test_failure(self):
        failed = self.jobs(failure=("Linux browser desktop", "Run viewport browser checks"))
        with self.assertRaisesRegex(ValueError, "run 101, attempt 1: Linux browser desktop.*fix and validate"):
            self.find([self.run_info(102), self.run_info(101, run_attempt=2)], attempts={(101, 1): failed})
        old_version = self.run_info(101, display_title="Release candidate v1.2.2", conclusion="failure")
        with self.assertRaisesRegex(ValueError, "fix and validate a new source commit"):
            self.find([self.run_info(102), old_version], attempts={(101, 1): failed})
        typo = self.run_info(101, display_title="Release candidate v01.2.2", conclusion="failure")
        self.assertEqual(self.find([self.run_info(102), typo], attempts={(101, 1): failed}), 102)

    def test_fresh_candidate_can_supersede_infrastructure_failure(self):
        failed = self.jobs(failure=("Native windows/amd64", "Retain native evidence and validation archives"))
        runs = [self.run_info(102), self.run_info(101, conclusion="failure")]
        self.assertEqual(self.find(runs, attempts={(101, 1): failed}), 102)

    def test_expired_or_missing_archives_cannot_be_promoted(self):
        with self.assertRaisesRegex(ValueError, "retained native archives"):
            self.find([self.run_info(101)], artifacts=[{"name": "native-linux-amd64", "expired": True}])

    def test_lookup_requires_complete_paginated_evidence(self):
        with patch("ci_release.subprocess.check_output", return_value='[{"total_count":2,"jobs":[{}]}]'):
            with self.assertRaisesRegex(ValueError, "incomplete candidate history"):
                github_entries("repos/owner/demo/actions/runs/101/attempts/1/jobs", "jobs")
