#!/bin/bash
set -e

COMPOSE_FILE="../opentelemetry-demo/compose.yaml"
OTEL_CONFIG="../opentelemetry-demo/src/otel-collector/otelcol-config.yml"

echo "Patching compose.yaml..."
if ! grep -q "absia:" "$COMPOSE_FILE"; then
  # Find the line number of 'services:' and insert absia block after it
  awk '/^services:/{print; print "  absia:\n    image: poonia98/absia:latest\n    ports:\n      - \"8081:8080\"\n      - \"4318:4318\"\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n    environment:\n      - ABSIA_OTLP_HTTP_PORT=4318\n    restart: unless-stopped"; next}1' "$COMPOSE_FILE" > tmp.yaml && mv tmp.yaml "$COMPOSE_FILE"
  echo "✅ Patched compose.yaml"
else
  echo "⚠️ compose.yaml already patched"
fi

echo "Patching otelcol-config.yml..."
if ! grep -q "otlphttp/absia:" "$OTEL_CONFIG"; then
  # Inject exporter definition
  awk '/exporters:/{print; print "  otlphttp/absia:\n    endpoint: http://absia:4318\n    tls:\n      insecure: true"; next}1' "$OTEL_CONFIG" > tmp.yml && mv tmp.yml "$OTEL_CONFIG"
  
  # Inject into metrics pipeline exporters
  awk '/metrics:/{in_metrics=1} in_metrics && /exporters:/{print; print "      - otlphttp/absia"; in_metrics=0; next}1' "$OTEL_CONFIG" > tmp2.yml && mv tmp2.yml "$OTEL_CONFIG"
  
  echo "✅ Patched otelcol-config.yml"
else
  echo "⚠️ otelcol-config.yml already patched"
fi

echo "Restarting services..."
cd ../opentelemetry-demo
docker compose up -d absia
docker compose restart otel-collector
cd ../Real-time-causal-inference-engine

echo "Done! Please run the verification script now."
