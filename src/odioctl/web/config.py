"""What `odioctl web` reads and writes, and the ports it speaks to.

One dataclass, no behaviour: `serve` builds it from the command line, and
both the services and the pages take their paths from it rather than from
module-level constants, so a test can point the whole server at a tmpdir.
"""

from __future__ import annotations

import os
from dataclasses import dataclass

from odioctl import state

DEFAULT_PORT = 8021
ODIO_UI_PORT = 8018  # odio-api's built-in dashboard, where upgrade progress is shown
UPGRADE_UNIT = "odio-upgrade.service"  # systemd --user unit shipped by this package


@dataclass
class WebConfig:
    bind: str = "0.0.0.0"
    port: int = DEFAULT_PORT
    state_path: str = state.SYSTEM_STATE_PATH
    config_txt: str | None = None  # None → dac.find_config_txt()
    odioctl_bin: str = os.environ.get("ODIOCTL_BIN", "/usr/bin/odioctl")
    upgrades_path: str | None = None  # None → sibling of a custom --state, else /var/cache

    def resolved_upgrades_path(self) -> str:
        if self.upgrades_path:
            return self.upgrades_path
        if self.state_path != state.SYSTEM_STATE_PATH:
            return os.path.join(os.path.dirname(self.state_path), "upgrades.json")
        return state.SYSTEM_UPGRADES_PATH
