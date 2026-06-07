#!/usr/bin/env python3
"""
Test tool that writes to stderr to verify logging behavior
"""

import json
import sys

def main():
    # Read the request from stdin
    request = json.load(sys.stdin)
    
    # Write to stderr
    sys.stderr.write("This is a test stderr message\n")
    sys.stderr.write("Error: Something went wrong!\n")
    sys.stderr.flush()
    
    # Write a proper response to stdout
    response = {
        "request_id": request.get("request_id", ""),
        "success": True,
        "content": [
            {
                "type": "text",
                "text": "Tool executed successfully"
            }
        ]
    }
    print(json.dumps(response))

if __name__ == "__main__":
    main()