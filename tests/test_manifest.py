import json
import os
import tempfile
import unittest
from typing import Any
from unittest.mock import MagicMock, patch

from odioctl import manifest


class ManifestUrlTests(unittest.TestCase):
    def test_latest_uses_releases_latest_path(self):
        self.assertEqual(
            manifest.manifest_url("latest"),
            f"https://github.com/{manifest.GITHUB_REPO}/releases/latest/download/manifest.json",
        )

    def test_specific_version_uses_release_tag(self):
        self.assertEqual(
            manifest.manifest_url("2026.5.0"),
            f"https://github.com/{manifest.GITHUB_REPO}/releases/download/2026.5.0/manifest.json",
        )

    def test_pr_prerelease_uses_pr_tag(self):
        # PR pre-releases tag as `pr-<N>` — odioctl must hit that asset.
        self.assertEqual(
            manifest.manifest_url("pr-42"),
            f"https://github.com/{manifest.GITHUB_REPO}/releases/download/pr-42/manifest.json",
        )

    def test_install_url_mirrors_manifest_url(self):
        self.assertTrue(
            manifest.install_url("latest").endswith("/releases/latest/download/install.sh")
        )
        self.assertTrue(manifest.install_url("2026.5.0").endswith("/download/2026.5.0/install.sh"))


class FetchManifestTests(unittest.TestCase):
    def test_returns_parsed_json_on_success(self):
        body = b'{"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}'
        fake = MagicMock()
        fake.__enter__.return_value.read.return_value = body
        with patch.object(manifest.urllib.request, "urlopen", return_value=fake):
            result = manifest.fetch_manifest("https://example.invalid/manifest.json")
        self.assertEqual(result, {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}})

    def test_returns_none_on_network_failure(self):
        # Any fetch failure falls back to "run all roles" by returning None —
        # derive_run_env emits no RUN_X overrides, so install.sh's defaults
        # take over.
        with patch.object(manifest.urllib.request, "urlopen", side_effect=OSError("boom")):
            self.assertIsNone(manifest.fetch_manifest("https://example.invalid/manifest.json"))


class ResolveManifestTests(unittest.TestCase):
    def _upgrades_file(self, payload: dict[str, Any]) -> str:
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump(payload, f)
            self.addCleanup(os.unlink, f.name)
            return f.name

    def test_returns_cached_manifest_on_version_match(self):
        # Daily check has cached the target manifest in upgrades.json.
        # `apply` for that version must reuse it without a network call.
        man = {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}
        path = self._upgrades_file({"latest": "2026.5.0", "manifest": man})
        with patch.object(manifest, "fetch_manifest") as fetch:
            result = manifest._resolve_manifest("2026.5.0", path)
        fetch.assert_not_called()
        self.assertEqual(result, man)

    def test_falls_back_to_fetch_on_version_mismatch(self):
        cached = {"odios": "2026.4.0", "roles": {}}
        target = {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}
        path = self._upgrades_file({"latest": "2026.4.0", "manifest": cached})
        with patch.object(manifest, "fetch_manifest", return_value=target) as fetch:
            result = manifest._resolve_manifest("2026.5.0", path)
        fetch.assert_called_once()
        self.assertEqual(result, target)

    def test_falls_back_to_fetch_when_file_missing(self):
        target = {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}
        with patch.object(manifest, "fetch_manifest", return_value=target) as fetch:
            result = manifest._resolve_manifest("2026.5.0", "/nonexistent/upgrades.json")
        fetch.assert_called_once()
        self.assertEqual(result, target)

    def test_falls_back_to_fetch_when_manifest_field_absent(self):
        path = self._upgrades_file({"latest": "2026.5.0", "roles": []})
        target = {"odios": "2026.5.0", "roles": {"mpd": "2026.5.0"}}
        with patch.object(manifest, "fetch_manifest", return_value=target) as fetch:
            result = manifest._resolve_manifest("2026.5.0", path)
        fetch.assert_called_once()
        self.assertEqual(result, target)


class UpgradesJsonReadersTests(unittest.TestCase):
    def _upgrades_file(self, payload: dict[str, Any]) -> str:
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump(payload, f)
            self.addCleanup(os.unlink, f.name)
            return f.name

    def test_resolve_version_prefers_explicit(self):
        self.assertEqual(manifest.resolve_version("2026.5.0", "/nonexistent"), "2026.5.0")

    def test_resolve_version_reads_latest_from_cache(self):
        path = self._upgrades_file({"latest": "2026.6.0"})
        self.assertEqual(manifest.resolve_version(None, path), "2026.6.0")

    def test_resolve_version_defaults_to_latest(self):
        self.assertEqual(manifest.resolve_version(None, "/nonexistent"), "latest")

    def test_upgrade_reported_reads_flag(self):
        self.assertTrue(manifest.upgrade_reported(self._upgrades_file({"upgrade_available": True})))
        self.assertFalse(
            manifest.upgrade_reported(self._upgrades_file({"upgrade_available": False}))
        )

    def test_upgrade_reported_true_when_missing(self):
        self.assertTrue(manifest.upgrade_reported("/nonexistent/upgrades.json"))
