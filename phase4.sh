#!/bin/bash
cd /workspaces/Real-time-causal-inference-engine

# Tear down anything currently running to prevent port conflicts
docker compose down -v 2>/dev/null
rm -rf microservices-demo

git clone https://github.com/GoogleCloudPlatform/microservices-demo
cd microservices-demo

# Use override file (Docker handles this automatically without breaking things)
cat > docker-compose.override.yml <<'EOF'
services:
  absia:
    image: poonia98/absia:latest
    ports: ['8080:8080']
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
  prometheus:
    image: prom/prometheus:latest
    ports: ['9090:9090']
    volumes: ['./prometheus.yml:/etc/prometheus/prometheus.yml']
  cadvisor:
    image: gcr.io/cadvisor/cadvisor:latest
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /sys:/sys:ro
      - /var/lib/docker/:/var/lib/docker:ro
    ports: ['8081:8080']
  grafana:
    image: grafana/grafana:latest
    ports: ['3000:3000']
EOF

# Create Prometheus Config
cat > prometheus.yml <<'EOF'
scrape_configs:
  - job_name: cadvisor
    static_configs:
      - targets: ['cadvisor:8080']
  - job_name: absia
    static_configs:
      - targets: ['absia:8080']
EOF

# Start the stack
docker compose up -d
echo "Waiting 5 minutes for Prometheus baseline data to populate..."
sleep 300

# Inject CPU Stress on checkoutservice and start timer
echo "--- INJECTING CPU STRESS (STARTING TIMER) ---"
START_TIME=$(date +%s)
docker exec microservices-demo-checkoutservice-1 sh -c 'apt-get update && apt-get install -y stress-ng 2>/dev/null; stress-ng --cpu 2 --timeout 120s &'

echo "Polling ABSIA for actionable root cause..."
while true; do
    ABSIA_RESP=$(curl -s -X POST http://localhost:8080/analyze -H 'Content-Type: application/json' -d '{"node_id":"checkoutservice","arrival_rate":200,"service_rate":80,"queue_length":15}')
    
    if echo "$ABSIA_RESP" | grep -q '"root_cause": "checkoutservice"'; then
        END_TIME=$(date +%s)
        ABSIA_LATENCY=$((END_TIME - START_TIME))
        echo "✅ ABSIA Actionable Root Cause found in: ${ABSIA_LATENCY} seconds"
        mkdir -p ../evidence/phase4
        echo "$ABSIA_RESP" > ../evidence/phase4/absia_response.json
        break
    fi
    sleep 5
done

echo "Polling Prometheus for CPU spike alert threshold..."
while true; do
    PROM_RESP=$(curl -s "http://localhost:9090/api/v1/query?query=rate(container_cpu_usage_seconds_total%5B1m%5D)")
    
    if echo "$PROM_RESP" | grep -E '"1\.[5-9][0-9]*"|"2\.[0-9]*"'; then
        END_TIME=$(date +%s)
        PROM_LATENCY=$((END_TIME - START_TIME))
        echo "🚨 Prometheus CPU threshold crossed in: ${PROM_LATENCY} seconds"
        echo "$PROM_RESP" > ../evidence/phase4/prometheus_response.json
        break
    fi
    sleep 5
done

echo "---------------------------------------------------"
echo "PHASE 4 TIMING RESULTS:"
echo "ABSIA Time-to-RCA:      ${ABSIA_LATENCY} seconds"
echo "Prometheus Time-to-RCA: ${PROM_LATENCY} seconds"
echo "---------------------------------------------------"
