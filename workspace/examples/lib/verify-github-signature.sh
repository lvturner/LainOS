#!/bin/bash

if [ -z "${SECRET:-}" ]; then
  echo "SECRET env var not set" >&2
  exit 1
fi

BODY=$(cat)

if [ -z "$WH_HEADER_X_HUB_SIGNATURE_256" ]; then
  echo "missing X-Hub-Signature-256 header" >&2
  exit 1
fi

EXPECTED="sha256=$(echo -n "$BODY" | openssl dgst -sha256 -hmac "$SECRET" | awk '{print $NF}')"

if [ "$WH_HEADER_X_HUB_SIGNATURE_256" != "$EXPECTED" ]; then
  echo "invalid signature" >&2
  exit 1
fi
