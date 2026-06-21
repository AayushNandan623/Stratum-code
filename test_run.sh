#!/bin/bash
curl -X POST http://localhost:8080/api/v1/orgs \
  -H "Content-Type: application/json" \
  -d '{"name": "Validation Org"}' > org.json

ORG_ID=$(grep -o '"id":"[^"]*' org.json | head -1 | cut -d'"' -f4)

curl -X POST http://localhost:8080/api/v1/orgs/$ORG_ID/stacks \
  -H "Authorization: Bearer mock-jwt" \
  -H "Content-Type: application/json" \
  -d '{"name": "Validation Stack", "description": "Validation Stack"}' > stack.json
  
STACK_ID=$(grep -o '"id":"[^"]*' stack.json | head -1 | cut -d'"' -f4)

curl -X POST http://localhost:8080/api/v1/stacks/$STACK_ID/runs \
  -H "Authorization: Bearer mock-jwt" \
  -H "Content-Type: application/json" \
  -d '{"type": "PLAN"}' > run.json
  
RUN_ID=$(grep -o '"id":"[^"]*' run.json | head -1 | cut -d'"' -f4)
echo "Created run: $RUN_ID"
