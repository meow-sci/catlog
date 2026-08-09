#!/usr/bin/env bash
# An interactive shell on the VM, over exactly the connection the playbooks use.
#
#   scripts/ssh.sh                 # a login shell
#   scripts/ssh.sh docker ps       # or one command
#
# Same key, same pinned host key, same user and port — all from infra/deploy.env
# through the generated ssh config. Your own ~/.ssh/config is not consulted, so
# what works here works in `make provision`, and what breaks here breaks there.
set -euo pipefail

# shellcheck source=lib/deploy-env.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/lib/deploy-env.sh"

catlog_ssh "$@"
