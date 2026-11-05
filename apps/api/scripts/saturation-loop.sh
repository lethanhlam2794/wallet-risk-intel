#!/usr/bin/env bash
# Stage 4 blind test saturation: N shuffled, uncached iterations of the full
# suite. Every iteration must pass -> N/10 score. -shuffle=on defeats
# order-dependent tests; -count=1 defeats result caching.
set -u
N=${1:-10}
pass=0
fail=0
for i in $(seq 1 "$N"); do
  if go test -count=1 -shuffle=on ./... > "/tmp/saturation-iter-$i.log" 2>&1; then
    pass=$((pass + 1))
    echo "iter $i: PASS"
  else
    fail=$((fail + 1))
    echo "iter $i: FAIL (see /tmp/saturation-iter-$i.log)"
    tail -15 "/tmp/saturation-iter-$i.log"
  fi
done
echo "SCORE: $pass/$N"
[ "$pass" -eq "$N" ]
