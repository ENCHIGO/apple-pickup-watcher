import subprocess
import unittest
from urllib.error import HTTPError

from release_branches import ensure_branch, published_tags, tag_commit


def missing(code=404):
    error = HTTPError("https://example.invalid", code, "test error", {}, None)
    error.close()
    return error


def ref(sha, kind="commit"):
    return {"object": {"type": kind, "sha": sha}}


class FakeAPI:
    def __init__(self, *responses):
        self.responses = list(responses)
        self.calls = []

    def __call__(self, method, path, data=None):
        self.calls.append((method, path, data))
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


class ReleaseBranchesTest(unittest.TestCase):
    def test_lightweight_tag_creates_branch_at_tag_commit(self):
        api = FakeAPI(ref("release-commit"), missing(), ref("release-commit"))
        result = ensure_branch(api, "v0.4.0")
        self.assertEqual(result["result"], "created")
        self.assertEqual(api.calls[-1], ("POST", "/git/refs", {
            "ref": "refs/heads/release/v0.4.0", "sha": "release-commit"}))

    def test_nested_annotated_tag_is_peeled(self):
        api = FakeAPI(ref("outer", "tag"), ref("inner", "tag"), ref("commit"))
        self.assertEqual(tag_commit(api, "v0.4.0"), "commit")
        self.assertEqual([call[1] for call in api.calls],
                         ["/git/ref/tags/v0.4.0", "/git/tags/outer", "/git/tags/inner"])

    def test_existing_hotfix_branch_is_never_reset(self):
        api = FakeAPI(ref("release-commit"), ref("newer-hotfix"))
        result = ensure_branch(api, "v0.4.0")
        self.assertEqual(result["branch_commit"], "newer-hotfix")
        self.assertEqual(result["result"], "preserved")
        self.assertTrue(all(call[0] == "GET" for call in api.calls))

    def test_pagination_skips_drafts_and_includes_prereleases(self):
        page = [{"tag_name": f"v{i}", "draft": False} for i in range(99)]
        page.append({"tag_name": "draft", "draft": True})
        api = FakeAPI(page, [{"tag_name": "v-next-rc1", "draft": False, "prerelease": True}])
        tags = published_tags(api)
        self.assertEqual(len(tags), 100)
        self.assertNotIn("draft", tags)
        self.assertEqual(tags[-1], "v-next-rc1")
        self.assertEqual(api.calls[-1][1], "/releases?per_page=100&page=2")

    def test_selected_release_uses_tag_not_target_commitish(self):
        api = FakeAPI({"tag_name": "v0.1.0", "target_commitish": "main", "draft": False})
        self.assertEqual(published_tags(api, "v0.1.0"), ["v0.1.0"])

    def test_draft_cannot_create_branch(self):
        with self.assertRaises(ValueError):
            published_tags(FakeAPI({"tag_name": "draft", "draft": True}), "draft")

    def test_missing_tag_does_not_fall_back_to_main(self):
        api = FakeAPI(missing())
        with self.assertRaises(HTTPError):
            ensure_branch(api, "v0.4.0")
        self.assertEqual(len(api.calls), 1)

    def test_noncommit_and_cyclic_tags_fail(self):
        for api in [FakeAPI(ref("blob", "blob")),
                    FakeAPI(ref("cycle", "tag"), ref("cycle", "tag"))]:
            with self.subTest(api=api), self.assertRaises(ValueError):
                tag_commit(api, "v0.4.0")

    def test_creation_race_preserves_winning_ref(self):
        api = FakeAPI(ref("release"), missing(), missing(422), ref("other-commit"))
        result = ensure_branch(api, "v0.4.0")
        self.assertEqual(result["branch_commit"], "other-commit")
        self.assertEqual(result["result"], "preserved")

    def test_api_errors_are_not_treated_as_missing_refs(self):
        for responses in [(ref("release"), missing(403)),
                          (ref("release"), missing(), missing(422), missing())]:
            with self.subTest(responses=responses), self.assertRaises(HTTPError):
                ensure_branch(FakeAPI(*responses), "v0.4.0")

    def test_invalid_ref_fails_before_any_api_call(self):
        for tag in ["../main", "v0.4.0\nmain", "v0.4.0.lock", ""]:
            api = FakeAPI()
            with self.subTest(tag=tag), self.assertRaises(subprocess.CalledProcessError):
                ensure_branch(api, tag)
            self.assertEqual(api.calls, [])


if __name__ == "__main__":
    unittest.main()
