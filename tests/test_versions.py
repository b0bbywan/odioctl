import unittest

from odioctl import versions


class ParseVersionTests(unittest.TestCase):
    def test_final_release_compares_above_rc(self):
        # 2026.5.0 > 2026.5.0rc1 > 2026.5.0b1 > 2026.5.0a1 — the phase axis
        # is what lets odioctl tell "ship-ready" from "iterating".
        pv = versions.parse_version
        self.assertGreater(pv("2026.5.0"), pv("2026.5.0rc1"))
        self.assertGreater(pv("2026.5.0rc1"), pv("2026.5.0b1"))
        self.assertGreater(pv("2026.5.0b1"), pv("2026.5.0a1"))

    def test_dev_commits_break_ties_within_a_phase(self):
        # build-manifest stamps `<base>-<N>-g<sha>` on commits past the tag —
        # smart-upgrade must treat those as newer than the bare tag.
        self.assertGreater(
            versions.parse_version("2026.5.0b1-3-gabc1234"),
            versions.parse_version("2026.5.0b1"),
        )

    def test_unparseable_returns_lowest_tuple(self):
        self.assertEqual(versions.parse_version("garbage"), (0,))
        self.assertLess(versions.parse_version("garbage"), versions.parse_version("2026.4.0a1"))
        self.assertEqual(versions.parse_version("latest"), (0,))

    def test_equal_versions_are_equal(self):
        self.assertEqual(versions.parse_version("2026.5.0"), versions.parse_version("2026.5.0"))


class IsDowngradeTests(unittest.TestCase):
    def test_target_below_state_is_downgrade(self):
        self.assertTrue(versions._is_downgrade("2026.4.0", "2026.5.0"))

    def test_target_above_state_is_not_downgrade(self):
        self.assertFalse(versions._is_downgrade("2026.5.0", "2026.4.0"))

    def test_target_equal_state_is_not_downgrade(self):
        self.assertFalse(versions._is_downgrade("2026.5.0", "2026.5.0"))

    def test_no_state_odios_is_not_downgrade(self):
        self.assertFalse(versions._is_downgrade("2026.5.0", None))

    def test_latest_target_is_not_downgrade(self):
        # "latest" parses to (0,); refuse-on-parse-miss would be wrong here.
        self.assertFalse(versions._is_downgrade("latest", "2026.5.0"))

    def test_unparseable_state_is_not_downgrade(self):
        self.assertFalse(versions._is_downgrade("2026.5.0", "garbage"))

    def test_git_describe_state_compares_correctly(self):
        # build-manifest stamps `<base>-<N>-g<sha>` past a tag; a target on
        # the bare tag is older than the dev-commit suffix.
        self.assertTrue(versions._is_downgrade("2026.5.0b1", "2026.5.0b1-3-gabc1234"))


class RoleUpToDateTests(unittest.TestCase):
    def test_missing_installed_or_target_is_not_up_to_date(self):
        self.assertFalse(versions._role_up_to_date(None, "2026.5.0", "2026.5.0"))
        self.assertFalse(versions._role_up_to_date("2026.5.0", None, "2026.5.0"))

    def test_target_newer_than_installed_is_not_up_to_date(self):
        self.assertFalse(versions._role_up_to_date("2026.4.0", "2026.5.0", "2026.5.0"))

    def test_target_ahead_of_state_odios_is_not_trusted(self):
        self.assertFalse(versions._role_up_to_date("2026.5.0b1", "2026.5.0b1", "2026.4.2b2-8-gabc"))

    def test_target_at_or_below_state_odios_is_up_to_date(self):
        self.assertTrue(versions._role_up_to_date("2026.5.0", "2026.5.0", "2026.5.0"))
