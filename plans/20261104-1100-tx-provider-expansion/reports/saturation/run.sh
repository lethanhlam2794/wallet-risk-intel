#!/usr/bin/env bash
# Blind test saturation harness — 10 randomized iterations over the
# pipeline's blast radius. Each iteration: -shuffle=on (random seed),
# -count=1 (no cache), packages visited in a freshly shuffled order.
# "Blind": the harness never names individual tests; ordering and seeds
# are randomized per iteration so no spec-name priming is possible.
set -u
cd /home/lethanhlam/Desktop/Profile/check-wallet
OUT=plans/20261104-1100-tx-provider-expansion/reports/saturation
mkdir -p "$OUT"
PKGS=(
  ./internal/provider/alchemy ./internal/provider/ankr
  ./internal/provider/bitquery ./internal/provider/blockscout
  ./internal/provider/chainbase ./internal/provider/chainscan
  ./internal/provider/covalent ./internal/provider/moralis
  ./internal/provider/nodereal ./internal/provider/oklink
  ./internal/provider/tatum ./internal/provider/tokenview
  ./internal/provider/resilience ./internal/provider/goplus
  ./internal/provider/chainabuse ./internal/provider/publicaml
  ./internal/provider/ofac ./internal/provider/walletattribution
  ./internal/transaction ./internal/config ./internal/network
  ./tests
)
PASS=0; FAIL=0
for i in $(seq 1 10); do
  ORDER=$(printf '%s\n' "${PKGS[@]}" | shuf | tr '\n' ' ')
  LOG="$OUT/iter-$(printf '%02d' "$i").log"
  START=$(date +%s)
  if go test -count=1 -shuffle=on $ORDER >"$LOG" 2>&1; then
    RESULT=PASS; PASS=$((PASS+1))
  else
    RESULT=FAIL; FAIL=$((FAIL+1))
  fi
  printf '%s | %s | %ss | %s\n' \
    "iter-$(printf '%02d' "$i")" "$RESULT" "$(( $(date +%s)-START ))" \
    "$(grep -c '^ok' "$LOG")/22 ok" >> "$OUT/summary.txt"
done
echo "SATURATION: $PASS/10 passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
