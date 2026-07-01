#!/bin/bash
set -e

echo "=========================================="
echo "STEP 1: Start Uptime Kuma"
echo "=========================================="
docker run -d --name uptime-kuma -p 3002:3001 \
  -v uptime-kuma-data:/app/data \
  --restart unless-stopped \
  louislam/uptime-kuma:1

echo "Waiting 30 seconds for Uptime Kuma to start..."
sleep 30
docker ps --format "{{.Names}}"

echo "=========================================="
echo "STEP 2: Start ABSIA"
echo "=========================================="
cd /workspaces/Real-time-causal-inference-engine
export PORT=8085
export ABSIA_WARMUP_OBSERVATIONS=10
sudo -E ./bin/absia > absia_uptime_debug.log 2>&1 &
echo "ABSIA started on port 8085"

echo "Waiting 60 seconds for ABSIA baseline..."
sleep 60

echo "=========================================="
echo "STEP 3: Verify discovery"
echo "=========================================="
mkdir -p evidence/uptime_test
curl -s http://localhost:8085/nodes > evidence/uptime_test/initial_discovery.json
cat evidence/uptime_test/initial_discovery.json | python3 -c "
import sys,json
d=json.load(sys.stdin)
print(f'Total nodes discovered: {d[\"node_count\"]}')
for n in d['nodes']:
    print(f'  {n[\"node_id\"]}')
"

echo "=========================================="
echo "STEP 4: Wait 5 minutes for real background activity"
echo "=========================================="
echo "Uptime Kuma scheduler runs automatically — no manual setup needed"
sleep 300

echo "=========================================="
echo "STEP 5: Query discovered service"
echo "=========================================="
NODES=$(curl -s http://localhost:8085/nodes | python3 -c "
import sys,json
d=json.load(sys.stdin)
for n in d['nodes']:
    print(n['node_id'])
")

for node in $NODES; do
  echo "--- $node ---"
  curl -s -X POST http://localhost:8085/analyze \
    -H "Content-Type: application/json" \
    -d "{\"node_id\":\"$node\"}" \
    > "evidence/uptime_test/result_${node}.json"
  
  cat "evidence/uptime_test/result_${node}.json" | python3 -c "
import sys,json
d=json.load(sys.stdin)
print(f'  root_cause: {d.get(\"root_cause\",\"none\")}')
print(f'  confidence: {d[\"confidence_score\"]:.2f}')
print(f'  state: {d.get(\"operational_state\",\"?\")}')
print(f'  compute_pressure: {d.get(\"compute_pressure\",0):.4f}')
print(f'  memory_pressure: {d.get(\"memory_pressure\",0):.4f}')
print(f'  time: {d[\"execution_time_ms\"]:.0f}ms')
"
done

echo "=========================================="
echo "ALL DONE — Evidence saved in evidence/uptime_test/"
echo "Uptime Kuma UI: http://localhost:3002"
echo "ABSIA API: http://localhost:8085"
echo "=========================================="