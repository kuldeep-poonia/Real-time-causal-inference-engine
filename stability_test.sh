#!/bin/bash
OUTFILE="/tmp/stability_results.json"
echo "[" > $OUTFILE
declare -A root_cause_count
total_time=0
total_confidence=0
success_count=0
fail_count=0
fallback_count=0
for i in $(seq 1 50); do
  RESPONSE=$(curl -s -X POST http://localhost:8081/analyze -H "Content-Type: application/json" -d '{"node_id":"checkout"}')
  ROOT_CAUSE=$(echo "$RESPONSE" | python3 -c "import sys,json
try:
 d=json.load(sys.stdin)
 print(d.get('root_cause','MISSING'))
except:
 print('PARSE_ERROR')" 2>/dev/null)
  CONFIDENCE=$(echo "$RESPONSE" | python3 -c "import sys,json
try:
 d=json.load(sys.stdin)
 print(d.get('confidence_score',-1))
except:
 print(-1)" 2>/dev/null)
  EXEC_TIME=$(echo "$RESPONSE" | python3 -c "import sys,json
try:
 d=json.load(sys.stdin)
 print(d.get('execution_time_ms',-1))
except:
 print(-1)" 2>/dev/null)
  FALLBACK=$(echo "$RESPONSE" | python3 -c "import sys,json
try:
 d=json.load(sys.stdin)
 print(d.get('fallback_triggered',False))
except:
 print('ERROR')" 2>/dev/null)
  echo "Run $i: root_cause=$ROOT_CAUSE confidence=$CONFIDENCE time=${EXEC_TIME}ms fallback=$FALLBACK"
  if [ "$ROOT_CAUSE" == "PARSE_ERROR" ] || [ "$ROOT_CAUSE" == "MISSING" ]; then
    fail_count=$((fail_count+1))
  else
    success_count=$((success_count+1))
    root_cause_count["$ROOT_CAUSE"]=$((${root_cause_count["$ROOT_CAUSE"]:-0}+1))
  fi
  if [ "$FALLBACK" == "True" ]; then
    fallback_count=$((fallback_count+1))
  fi
  total_time=$(echo "$total_time + $EXEC_TIME" | bc 2>/dev/null || echo $total_time)
  total_confidence=$(echo "$total_confidence + $CONFIDENCE" | bc 2>/dev/null || echo $total_confidence)
  echo "$RESPONSE" >> $OUTFILE
  echo "," >> $OUTFILE
  sleep 6
done
sed -i '$ s/,$//' $OUTFILE
echo "]" >> $OUTFILE
echo ""
echo "STABILITY TEST SUMMARY - 50 RUNS"
echo "Successful: $success_count / 50"
echo "Failed: $fail_count / 50"
echo "Fallback triggered: $fallback_count / 50"
echo "Root cause distribution:"
for key in "${!root_cause_count[@]}"; do
  echo "  $key: ${root_cause_count[$key]} times"
done
avg_time=$(echo "scale=2; $total_time / 50" | bc 2>/dev/null)
avg_conf=$(echo "scale=4; $total_confidence / 50" | bc 2>/dev/null)
echo "Average execution_time_ms: $avg_time"
echo "Average confidence_score: $avg_conf"
echo "Raw results: $OUTFILE"