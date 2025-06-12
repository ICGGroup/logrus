#!/bin/bash

# Set the URL for the event router
EVENT_ROUTER_URL="https://logevent.icgdevspace.com"

# Send a test event
curl -X POST "$EVENT_ROUTER_URL/eventsink" -H "Content-Type: application/json" -d '{
  "level": "info",
  "message": "Test event",
  "module": "github.com/icggroup/Loves-Portal",
  "data": {
    "key": "value"
  }
}'