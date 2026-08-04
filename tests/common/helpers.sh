#!/bin/bash
# Common helper functions for integration test scripts.

# Check for Docker and required containers.
# Exits 0 if docker is missing or the container is not running (unless
# RUN_INTEGRATION_TESTS=1 is set).
check_docker_container() {
    local container_name="$1"
    if ! command -v docker >/dev/null 2>&1; then
        echo "Docker not installed. Skipping integration tests."
        exit 0
    fi
    if [ "$(docker ps --filter "name=$container_name" --format '{{.Names}}' | grep -c "$container_name")" -eq 0 ]; then
        if [ "${RUN_INTEGRATION_TESTS:-}" != "1" ]; then
            echo "Container $container_name not running. Set RUN_INTEGRATION_TESTS=1 to run anyway."
            exit 0
        fi
    fi
}
