#!/bin/bash

# Test script to verify that stderr from Python tools is properly logged by the Go server

echo "=============================================="
echo "Testing stderr logging from Python subprocess"
echo "=============================================="

# Check if we're in the right directory
if [ ! -f "go.mod" ] || [ ! -d "tools" ]; then
    echo "Error: This script must be run from the mcp-go root directory"
    exit 1
fi

# Create a test tool that writes to stderr if it doesn't exist
TEST_TOOL_DIR="tools/test_stderr_tool"
if [ ! -d "$TEST_TOOL_DIR" ]; then
    echo "Creating test tool..."
    mkdir -p "$TEST_TOOL_DIR"
    
    cat > "$TEST_TOOL_DIR/main.py" << 'EOF'
#!/usr/bin/env python3
import json
import sys

def main():
    # Read the request from stdin
    request = json.load(sys.stdin)
    
    # Write multiple stderr messages
    sys.stderr.write("STDERR: Starting test tool execution\n")
    sys.stderr.write("STDERR: Simulating an error condition\n")
    sys.stderr.write("STDERR: This traceback would be lost if stderr is not captured properly\n")
    sys.stderr.flush()
    
    # Write a proper response to stdout
    response = {
        "request_id": request.get("request_id", ""),
        "success": True,
        "content": [
            {
                "type": "text",
                "text": "Test completed successfully with stderr output"
            }
        ]
    }
    print(json.dumps(response))

if __name__ == "__main__":
    main()
EOF

    cat > "$TEST_TOOL_DIR/tool.yaml" << EOF
name: test_stderr_tool
description: Test tool that writes to stderr for logging verification
command: python3
args:
  - tools/test_stderr_tool/main.py
timeout: 30s
input_schema:
  type: object
  properties: {}
  required: []
EOF

    chmod +x "$TEST_TOOL_DIR/main.py"
fi

echo "Test tool created at: $TEST_TOOL_DIR"

# Build the server
echo "Building MCP server..."
go build -o bin/mcp-server ./cmd/server || {
    echo "Failed to build server"
    exit 1
}

echo "Test setup completed successfully!"
echo ""
echo "To verify stderr logging:"
echo "1. Start the server: ./bin/mcp-server"
echo "2. In another terminal, run: curl -X POST http://localhost:8080/execute -H 'Content-Type: application/json' -d '{\"tool\": \"test_stderr_tool\", \"arguments\": {}}'"
echo "3. Check the server logs for stderr output with request_id correlation"
echo ""
echo "Expected log output should contain lines like:"
echo "WARN ... Subprocess stderr output tool=test_stderr_tool stderr=\"STDERR: Starting test tool execution...\""