#!/usr/bin/env sh
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 alibaba/open-code-review Contributors

set -eu

if [ "$#" -ne 5 ]; then
  echo "usage: remote-branch-review.sh CONFIG_DIR FROM_REV FROM_TARGET TO_REV TO_TARGET" >&2
  exit 2
fi

config_dir=$1
from_revision=$2
from_target=$3
to_revision=$4
to_target=$5

exec ocr review \
  --repo "$config_dir" \
  --from "$from_revision" \
  --to "$to_revision" \
  --svn-from-target "$from_target" \
  --svn-to-target "$to_target" \
  --background "Exact remote SVN comparison ${from_revision}:${to_revision}" \
  --format json \
  --audience agent
