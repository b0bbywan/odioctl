import contextlib
import io
import unittest
from unittest.mock import patch

from odioctl import __version__, cli, netinfo


class CliTests(unittest.TestCase):
    def test_no_command_prints_help_and_returns_2(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(cli.main([]), 2)
        self.assertIn("usage: odioctl", out.getvalue())

    def test_version_flag(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out), self.assertRaises(SystemExit) as cm:
            cli.main(["--version"])
        self.assertEqual(cm.exception.code, 0)
        self.assertIn(__version__, out.getvalue())

    def test_every_subcommand_has_help(self):
        for argv in (
            ["upgrade", "check"],
            ["upgrade", "apply"],
            ["upgrade", "verify"],
            ["pwa-url"],
            ["components"],
            ["dac"],
            ["web"],
        ):
            with self.subTest(argv=argv):
                out = io.StringIO()
                with contextlib.redirect_stdout(out), self.assertRaises(SystemExit) as cm:
                    cli.main([*argv, "--help"])
                self.assertEqual(cm.exception.code, 0)
                self.assertIn("usage: odioctl " + " ".join(argv), out.getvalue())

    def test_upgrade_without_subcommand_errors(self):
        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as cm:
            cli.main(["upgrade"])
        self.assertEqual(cm.exception.code, 2)

    def test_pwa_url_prints_ip_link(self):
        out = io.StringIO()
        with (
            patch.object(netinfo, "default_route_ip", return_value="192.168.1.10"),
            contextlib.redirect_stdout(out),
        ):
            self.assertEqual(cli.main(["pwa-url"]), 0)
        self.assertEqual(out.getvalue().strip(), "https://pwa.odio.love/#/i/192.168.1.10")

    def test_pwa_url_falls_back_to_bare_url(self):
        out = io.StringIO()
        with (
            patch.object(netinfo, "default_route_ip", return_value=None),
            contextlib.redirect_stdout(out),
        ):
            cli.main(["pwa-url"])
        self.assertEqual(out.getvalue().strip(), "https://pwa.odio.love")


class NetinfoTests(unittest.TestCase):
    def test_default_route_ip_parses_src(self):
        fake = netinfo.subprocess.CompletedProcess(
            ["ip"], 0, stdout="1.1.1.1 via 192.168.1.1 dev eth0 src 192.168.1.42 uid 1000\n"
        )
        with patch.object(netinfo.subprocess, "run", return_value=fake):
            self.assertEqual(netinfo.default_route_ip(), "192.168.1.42")

    def test_default_route_ip_none_when_ip_missing(self):
        with patch.object(netinfo.subprocess, "run", side_effect=FileNotFoundError):
            self.assertIsNone(netinfo.default_route_ip())
