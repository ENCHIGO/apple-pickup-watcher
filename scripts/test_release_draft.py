import unittest

from release_draft import PLATFORMS, ReleaseError, build_manifest, choose_draft

V = "Apple.Pickup.Watcher_0.6.2"
INSTALLERS = [
    f"{V}_aarch64.app.tar.gz",
    f"{V}_x64.app.tar.gz",
    f"{V}_amd64.AppImage",
    f"{V}_amd64.deb",
    "Apple.Pickup.Watcher-0.6.2-1.x86_64.rpm",
    f"{V}_x64-setup.exe",
    f"{V}_x64_en-US.msi",
]
# Assets that carry no updater entry must be ignored, not matched by suffix.
OTHERS = [f"{V}_aarch64.dmg", f"{V}_x64.dmg", "apw-cli-v0.6.2-x86_64-apple-darwin.tar.gz", "latest.json"]


def full_draft():
    names = INSTALLERS + [n + ".sig" for n in INSTALLERS] + OTHERS
    return names, {n + ".sig": f"sig-of-{n}" for n in INSTALLERS}


def manifest(names, sigs):
    return build_manifest(names, sigs, "v0.6.2", "o/r", "0.6.2", "notes", "2026-10-15T00:00:00Z")


class ChooseDraftTest(unittest.TestCase):
    def test_no_release_yet(self):
        self.assertIsNone(choose_draft([{"id": 1, "tag_name": "v0.6.1", "draft": False}], "v0.6.2"))

    def test_reuses_the_single_draft(self):
        self.assertEqual(choose_draft([{"id": 7, "tag_name": "v0.6.2", "draft": True}], "v0.6.2"), 7)

    def test_two_drafts_is_the_v062_failure_and_must_stop(self):
        releases = [{"id": 7, "tag_name": "v0.6.2", "draft": True},
                    {"id": 8, "tag_name": "v0.6.2", "draft": True}]
        with self.assertRaisesRegex(ReleaseError, "2 个草稿"):
            choose_draft(releases, "v0.6.2")

    def test_never_touches_a_published_release(self):
        with self.assertRaisesRegex(ReleaseError, "公开"):
            choose_draft([{"id": 7, "tag_name": "v0.6.2", "draft": False}], "v0.6.2")


class BuildManifestTest(unittest.TestCase):
    def test_full_draft_gives_all_eleven_platforms_with_download_urls(self):
        names, sigs = full_draft()
        m = manifest(names, sigs)
        self.assertEqual(sorted(m["platforms"]), sorted(k for k, _ in PLATFORMS))
        self.assertEqual(len(m["platforms"]), 11)
        win = m["platforms"]["windows-x86_64"]
        self.assertEqual(win["url"], f"https://github.com/o/r/releases/download/v0.6.2/{V}_x64-setup.exe")
        self.assertEqual(win["signature"], f"sig-of-{V}_x64-setup.exe")
        self.assertEqual(m["platforms"]["windows-x86_64-msi"]["url"].rsplit("/", 1)[1], f"{V}_x64_en-US.msi")
        self.assertEqual(m["platforms"]["darwin-x86_64"]["url"].rsplit("/", 1)[1], f"{V}_x64.app.tar.gz")
        self.assertEqual((m["version"], m["notes"]), ("0.6.2", "notes"))

    def test_half_a_draft_fails_and_names_what_is_missing(self):
        # What the second v0.6.2 draft looked like: only Linux and Intel Mac.
        names, sigs = full_draft()
        names = [n for n in names if "aarch64" not in n and "x64-setup" not in n and "msi" not in n]
        with self.assertRaises(ReleaseError) as ctx:
            manifest(names, sigs)
        text = str(ctx.exception)
        for key in ("darwin-aarch64", "windows-x86_64", "windows-x86_64-msi"):
            self.assertIn(key, text)

    def test_installer_without_signature_fails(self):
        names, sigs = full_draft()
        del sigs[f"{V}_amd64.deb.sig"]
        with self.assertRaisesRegex(ReleaseError, "linux-x86_64-deb"):
            manifest(names, sigs)

    def test_two_installers_for_one_platform_fails(self):
        names, sigs = full_draft()
        names.append("Apple.Pickup.Watcher_0.6.1_amd64.deb")
        with self.assertRaisesRegex(ReleaseError, "linux-x86_64-deb"):
            manifest(names, sigs)


if __name__ == "__main__":
    unittest.main()
