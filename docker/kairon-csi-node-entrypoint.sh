#!/bin/sh
# Copyright 2026 Zyvor · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
#
# open-iscsi's iscsiadm CLI needs a running iscsid to actually perform a
# login -- there's no init system inside this container to start it any
# other way, and requiring the host to already run one would make this
# first cut depend on operator-specific node setup this chart can't
# verify. Self-contained instead: start iscsid here, then exec the real
# binary. See docs/guides/machine-storage-csi.md.
set -e
/sbin/iscsid
exec /kairon-csi-node "$@"
